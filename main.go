// worldtime - Print local time for any city in the world.
//
// City resolution works in two stages:
//
//  1. Fast local lookup: city name matched against the embedded IANA zone
//     list + a small alias table. Handles common cases instantly, offline,
//     with zero filesystem access — works on any OS.
//
//  2. Geocode fallback: if the local lookup fails, the query is sent to
//     Nominatim (OpenStreetMap) to get coordinates, then the tzf library
//     converts those coordinates to an IANA timezone using embedded
//     timezone boundary data. This handles any city, neighbourhood,
//     address, or "city, state/country" disambiguation string.
//
// Build:
//
//	go mod tidy
//	go build -buildvcs=false -trimpath -ldflags="-s -w" -o worldtime .
//
// Usage:
//
//	worldtime 'San Francisco'
//	worldtime 'Manchester, NH'
//	worldtime 'London, Ontario'
//	worldtime Tokyo London 'New York'
//	worldtime --list
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // embed IANA tzdata rules — no host zoneinfo needed

	"github.com/ringsaturn/tzf"
)

// ── Alias table ─────────────────────────────────────────────────────────────
//
// Only needed for cities that share a timezone zone with a differently-named
// IANA representative (e.g. San Francisco → America/Los_Angeles).
// The geocode fallback handles everything else automatically.

var aliases = []struct{ city, tz string }{
	// US Pacific
	{"San Francisco", "America/Los_Angeles"},
	{"SF", "America/Los_Angeles"},
	{"Silicon Valley", "America/Los_Angeles"},
	{"San Jose", "America/Los_Angeles"},
	{"Oakland", "America/Los_Angeles"},
	{"Sacramento", "America/Los_Angeles"},
	{"San Diego", "America/Los_Angeles"},
	// US Mountain
	{"Salt Lake City", "America/Denver"},
	{"Albuquerque", "America/Denver"},
	// US Central
	{"Houston", "America/Chicago"},
	{"Dallas", "America/Chicago"},
	{"Austin", "America/Chicago"},
	{"San Antonio", "America/Chicago"},
	{"Minneapolis", "America/Chicago"},
	{"Kansas City", "America/Chicago"},
	{"New Orleans", "America/Chicago"},
	{"Memphis", "America/Chicago"},
	{"Nashville", "America/Chicago"},
	{"Milwaukee", "America/Chicago"},
	// US Eastern
	{"New York", "America/New_York"},
	{"NYC", "America/New_York"},
	{"Manhattan", "America/New_York"},
	{"Brooklyn", "America/New_York"},
	{"Philadelphia", "America/New_York"},
	{"Washington DC", "America/New_York"},
	{"Atlanta", "America/New_York"},
	{"Miami", "America/New_York"},
	{"Orlando", "America/New_York"},
	{"Charlotte", "America/New_York"},
	// Canada
	{"Toronto", "America/Toronto"},
	{"Ottawa", "America/Toronto"},
	{"Montreal", "America/Toronto"},
	{"Vancouver", "America/Vancouver"},
	{"Calgary", "America/Edmonton"},
	// Mexico
	{"Mexico City", "America/Mexico_City"},
	{"Guadalajara", "America/Mexico_City"},
	// South America
	{"Rio de Janeiro", "America/Sao_Paulo"},
	{"Brasilia", "America/Sao_Paulo"},
	{"Buenos Aires", "America/Argentina/Buenos_Aires"},
	// Europe
	{"London", "Europe/London"},
	{"Barcelona", "Europe/Madrid"},
	{"Lyon", "Europe/Paris"},
	{"Hamburg", "Europe/Berlin"},
	{"Munich", "Europe/Berlin"},
	{"Frankfurt", "Europe/Berlin"},
	{"Cologne", "Europe/Berlin"},
	{"Geneva", "Europe/Zurich"},
	{"Milan", "Europe/Rome"},
	{"Naples", "Europe/Rome"},
	{"Kyiv", "Europe/Kyiv"},
	{"Kiev", "Europe/Kyiv"},
	{"Saint Petersburg", "Europe/Moscow"},
	{"St Petersburg", "Europe/Moscow"},
	// Middle East
	{"Tel Aviv", "Asia/Jerusalem"},
	{"Jeddah", "Asia/Riyadh"},
	{"Abu Dhabi", "Asia/Dubai"},
	{"Kuwait City", "Asia/Kuwait"},
	// South Asia
	{"New Delhi", "Asia/Kolkata"},
	{"Delhi", "Asia/Kolkata"},
	{"Mumbai", "Asia/Kolkata"},
	{"Bombay", "Asia/Kolkata"},
	{"Bangalore", "Asia/Kolkata"},
	{"Bengaluru", "Asia/Kolkata"},
	{"Chennai", "Asia/Kolkata"},
	{"Calcutta", "Asia/Kolkata"},
	{"Islamabad", "Asia/Karachi"},
	{"Lahore", "Asia/Karachi"},
	// East Asia
	{"Beijing", "Asia/Shanghai"},
	{"Guangzhou", "Asia/Shanghai"},
	{"Shenzhen", "Asia/Shanghai"},
	{"Hong Kong", "Asia/Hong_Kong"},
	{"Osaka", "Asia/Tokyo"},
	{"Kyoto", "Asia/Tokyo"},
	{"Busan", "Asia/Seoul"},
	{"Ho Chi Minh City", "Asia/Ho_Chi_Minh"},
	{"Saigon", "Asia/Ho_Chi_Minh"},
	// Oceania
	{"Sydney", "Australia/Sydney"},
	{"Canberra", "Australia/Sydney"},
	{"Auckland", "Pacific/Auckland"},
	{"Wellington", "Pacific/Auckland"},
}

// ── Local database ──────────────────────────────────────────────────────────

type entry struct {
	city string
	tz   string
}

// loadDB builds the city→timezone database entirely from the embedded IANA
// zone list — no filesystem access, no platform assumptions. Works identically
// on Linux, macOS, Windows, Alpine, scratch containers, etc.
//
// The zone list (ianaZones below) is sourced from IANA tzdb and matches what
// Go's time/tzdata package embeds. To pick up new zones, just upgrade Go:
//
//	go get -u && go mod tidy
func loadDB() []entry {
	var db []entry
	for _, tz := range ianaZones {
		parts := strings.Split(tz, "/")
		city := strings.ReplaceAll(parts[len(parts)-1], "_", " ")
		db = append(db, entry{city: city, tz: tz})
	}
	for _, a := range aliases {
		db = append(db, entry{city: a.city, tz: a.tz})
	}
	return db
}

// ── Local lookup ─────────────────────────────────────────────────────────────

func findLocal(db []entry, query string) *entry {
	q := strings.ToLower(query)
	for pass := 0; pass < 4; pass++ {
		for i := len(db) - 1; i >= 0; i-- {
			city := strings.ToLower(db[i].city)
			tz := strings.ToLower(db[i].tz)
			switch pass {
			case 0:
				if city == q {
					return &db[i]
				}
			case 1:
				if strings.HasPrefix(city, q) {
					return &db[i]
				}
			case 2:
				if strings.Contains(city, q) {
					return &db[i]
				}
			case 3:
				if strings.Contains(tz, q) {
					return &db[i]
				}
			}
		}
	}
	return nil
}

// ── Geocode fallback ─────────────────────────────────────────────────────────
//
// Calls Nominatim (OpenStreetMap) to resolve any city/address string to
// coordinates, then uses the tzf library to convert lat/lon → IANA timezone.
// No API key required. Nominatim's usage policy requires a descriptive
// User-Agent and asks for a max of 1 request/second (fine for a CLI tool).

type nominatimResult struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	DisplayName string `json:"display_name"`
}

func geocodeLookup(finder tzf.F, query string) (tzID string, displayName string, err error) {
	apiURL := "https://nominatim.openstreetmap.org/search?format=json&limit=1&q=" +
		url.QueryEscape(query)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "worldtime-cli/1.0 (https://github.com/dnj12345/worldtime)")
	req.Header.Set("Accept-Language", "en")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("geocode request failed: %w", err)
	}
	defer resp.Body.Close()

	var results []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return "", "", fmt.Errorf("geocode response parse failed: %w", err)
	}
	if len(results) == 0 {
		return "", "", fmt.Errorf("city not found: %q", query)
	}

	var lat, lon float64
	fmt.Sscanf(results[0].Lat, "%f", &lat)
	fmt.Sscanf(results[0].Lon, "%f", &lon)

	tz := finder.GetTimezoneName(lon, lat)
	if tz == "" {
		return "", "", fmt.Errorf("could not determine timezone for coordinates (%.4f, %.4f)", lat, lon)
	}
	return tz, results[0].DisplayName, nil
}

// ── Time formatting ──────────────────────────────────────────────────────────

func printTime(tzID string) error {
	loc, err := time.LoadLocation(tzID)
	if err != nil {
		return fmt.Errorf("unknown timezone %q: %w", tzID, err)
	}
	fmt.Println(time.Now().In(loc).Format("Mon Jan _2 15:04:05 MST 2006"))
	return nil
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr,
			"Usage: %s <city> [city2 ...]\n"+
				"       %s --list\n\n"+
				"Examples:\n"+
				"  %s Tokyo\n"+
				"  %s 'Manchester, NH'\n"+
				"  %s 'London, Ontario' Paris Tokyo\n",
			os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		os.Exit(1)
	}

	db := loadDB()

	if os.Args[1] == "--list" {
		sorted := make([]entry, len(db))
		copy(sorted, db)
		sort.Slice(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].city) < strings.ToLower(sorted[j].city)
		})
		fmt.Printf("%-35s  %s\n", "City", "Timezone")
		fmt.Printf("%-35s  %s\n", "----", "--------")
		for _, e := range sorted {
			fmt.Printf("%-35s  %s\n", e.city, e.tz)
		}
		fmt.Printf("\n%d entries in local DB (unknown cities resolved via OpenStreetMap)\n", len(db))
		return
	}

	// Initialise the tzf timezone finder (used only for geocode fallback).
	// This embeds ~5MB of timezone boundary data in the binary.
	finder, err := tzf.NewDefaultFinder()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to init timezone finder:", err)
		os.Exit(1)
	}

	multi := len(os.Args) > 2
	exitCode := 0

	for _, query := range os.Args[1:] {
		if multi {
			fmt.Printf("%-22s  ", query)
		}

		// Stage 1: fast local lookup
		if e := findLocal(db, query); e != nil {
			if err := printTime(e.tz); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				exitCode = 1
			}
			continue
		}

		// Stage 2: geocode via Nominatim + tzf
		tzID, displayName, err := geocodeLookup(finder, query)
		if err != nil {
			if multi {
				fmt.Println()
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			exitCode = 1
			continue
		}

		// Print resolved location on stderr so stdout stays clean
		fmt.Fprintf(os.Stderr, "→ resolved %q to %s (%s)\n", query, displayName, tzID)
		if err := printTime(tzID); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			exitCode = 1
		}
	}

	os.Exit(exitCode)
}

// ── Embedded IANA zone list ──────────────────────────────────────────────────
//
// Canonical list of IANA timezone IDs, matching what Go's time/tzdata embeds.
// City names for the local DB are derived from the last path component at
// startup in loadDB() — no filesystem access needed.
//
// To update: upgrade Go and run `go mod tidy`. The list itself rarely changes;
// IANA typically adds 0-3 zones per year.
//
// Source: https://data.iana.org/time-zones/tzdb-latest.tar.lz

var ianaZones = strings.Fields(`
Africa/Abidjan Africa/Accra Africa/Addis_Ababa Africa/Algiers Africa/Asmara
Africa/Bamako Africa/Bangui Africa/Banjul Africa/Bissau Africa/Blantyre
Africa/Brazzaville Africa/Bujumbura Africa/Cairo Africa/Casablanca Africa/Ceuta
Africa/Conakry Africa/Dakar Africa/Dar_es_Salaam Africa/Djibouti Africa/Douala
Africa/El_Aaiun Africa/Freetown Africa/Gaborone Africa/Harare Africa/Johannesburg
Africa/Juba Africa/Kampala Africa/Khartoum Africa/Kigali Africa/Kinshasa
Africa/Lagos Africa/Libreville Africa/Lome Africa/Luanda Africa/Lubumbashi
Africa/Lusaka Africa/Malabo Africa/Maputo Africa/Maseru Africa/Mbabane
Africa/Mogadishu Africa/Monrovia Africa/Nairobi Africa/Ndjamena Africa/Niamey
Africa/Nouakchott Africa/Ouagadougou Africa/Porto-Novo Africa/Sao_Tome
Africa/Tripoli Africa/Tunis Africa/Windhoek
America/Adak America/Anchorage America/Anguilla America/Antigua America/Araguaina
America/Argentina/Buenos_Aires America/Argentina/Catamarca America/Argentina/Cordoba
America/Argentina/Jujuy America/Argentina/La_Rioja America/Argentina/Mendoza
America/Argentina/Rio_Gallegos America/Argentina/Salta America/Argentina/San_Juan
America/Argentina/San_Luis America/Argentina/Tucuman America/Argentina/Ushuaia
America/Aruba America/Asuncion America/Atikokan America/Bahia
America/Bahia_Banderas America/Barbados America/Belem America/Belize
America/Blanc-Sablon America/Boa_Vista America/Bogota America/Boise
America/Cambridge_Bay America/Campo_Grande America/Cancun America/Caracas
America/Cayenne America/Cayman America/Chicago America/Chihuahua
America/Ciudad_Juarez America/Costa_Rica America/Creston America/Cuiaba
America/Curacao America/Danmarkshavn America/Dawson America/Dawson_Creek
America/Denver America/Detroit America/Dominica America/Edmonton
America/Eirunepe America/El_Salvador America/Fortaleza America/Glace_Bay
America/Goose_Bay America/Grand_Turk America/Grenada America/Guadeloupe
America/Guatemala America/Guayaquil America/Guyana America/Halifax
America/Havana America/Hermosillo America/Indiana/Indianapolis America/Indiana/Knox
America/Indiana/Marengo America/Indiana/Petersburg America/Indiana/Tell_City
America/Indiana/Vevay America/Indiana/Vincennes America/Indiana/Winamac
America/Inuvik America/Iqaluit America/Jamaica America/Juneau
America/Kentucky/Louisville America/Kentucky/Monticello America/Kralendijk
America/La_Paz America/Lima America/Los_Angeles America/Lower_Princes
America/Maceio America/Managua America/Manaus America/Marigot America/Martinique
America/Matamoros America/Mazatlan America/Menominee America/Merida
America/Metlakatla America/Mexico_City America/Miquelon America/Moncton
America/Monterrey America/Montevideo America/Montserrat America/Nassau
America/New_York America/Nome America/Noronha America/North_Dakota/Beulah
America/North_Dakota/Center America/North_Dakota/New_Salem America/Nuuk
America/Ojinaga America/Panama America/Paramaribo America/Phoenix
America/Port-au-Prince America/Port_of_Spain America/Porto_Velho
America/Puerto_Rico America/Punta_Arenas America/Rankin_Inlet America/Recife
America/Regina America/Resolute America/Rio_Branco America/Santarem
America/Santiago America/Santo_Domingo America/Sao_Paulo America/Scoresbysund
America/Sitka America/St_Barthelemy America/St_Johns America/St_Kitts
America/St_Lucia America/St_Thomas America/St_Vincent America/Swift_Current
America/Tegucigalpa America/Thule America/Tijuana America/Toronto
America/Tortola America/Vancouver America/Whitehorse America/Winnipeg
America/Yakutat
Antarctica/Casey Antarctica/Davis Antarctica/Mawson Antarctica/McMurdo
Antarctica/Palmer Antarctica/Rothera Antarctica/Syowa Antarctica/Troll
Antarctica/Vostok
Arctic/Longyearbyen
Asia/Aden Asia/Almaty Asia/Amman Asia/Anadyr Asia/Aqtau Asia/Aqtobe
Asia/Ashgabat Asia/Atyrau Asia/Baghdad Asia/Bahrain Asia/Baku Asia/Bangkok
Asia/Barnaul Asia/Beirut Asia/Bishkek Asia/Brunei Asia/Chita Asia/Choibalsan
Asia/Colombo Asia/Damascus Asia/Dhaka Asia/Dili Asia/Dubai Asia/Dushanbe
Asia/Famagusta Asia/Gaza Asia/Hebron Asia/Ho_Chi_Minh Asia/Hong_Kong Asia/Hovd
Asia/Irkutsk Asia/Jakarta Asia/Jayapura Asia/Jerusalem Asia/Kabul
Asia/Kamchatka Asia/Karachi Asia/Kathmandu Asia/Khandyga Asia/Kolkata
Asia/Krasnoyarsk Asia/Kuala_Lumpur Asia/Kuching Asia/Kuwait Asia/Macau
Asia/Magadan Asia/Makassar Asia/Manila Asia/Muscat Asia/Nicosia
Asia/Novokuznetsk Asia/Novosibirsk Asia/Omsk Asia/Oral Asia/Phnom_Penh
Asia/Pontianak Asia/Pyongyang Asia/Qatar Asia/Qostanay Asia/Qyzylorda
Asia/Riyadh Asia/Sakhalin Asia/Samarkand Asia/Seoul Asia/Shanghai
Asia/Singapore Asia/Srednekolymsk Asia/Taipei Asia/Tashkent Asia/Tbilisi
Asia/Tehran Asia/Thimphu Asia/Tokyo Asia/Tomsk Asia/Ulaanbaatar Asia/Urumqi
Asia/Ust-Nera Asia/Vientiane Asia/Vladivostok Asia/Yakutsk Asia/Yangon
Asia/Yekaterinburg Asia/Yerevan
Atlantic/Azores Atlantic/Bermuda Atlantic/Canary Atlantic/Cape_Verde
Atlantic/Faroe Atlantic/Madeira Atlantic/Reykjavik Atlantic/South_Georgia
Atlantic/St_Helena Atlantic/Stanley
Australia/Adelaide Australia/Brisbane Australia/Broken_Hill Australia/Darwin
Australia/Eucla Australia/Hobart Australia/Lindeman Australia/Lord_Howe
Australia/Melbourne Australia/Perth Australia/Sydney
Europe/Amsterdam Europe/Andorra Europe/Astrakhan Europe/Athens Europe/Belgrade
Europe/Berlin Europe/Bratislava Europe/Brussels Europe/Bucharest Europe/Budapest
Europe/Busingen Europe/Chisinau Europe/Copenhagen Europe/Dublin Europe/Gibraltar
Europe/Guernsey Europe/Helsinki Europe/Isle_of_Man Europe/Istanbul Europe/Jersey
Europe/Kaliningrad Europe/Kirov Europe/Kyiv Europe/Lisbon Europe/Ljubljana
Europe/London Europe/Luxembourg Europe/Madrid Europe/Malta Europe/Mariehamn
Europe/Minsk Europe/Monaco Europe/Moscow Europe/Nicosia Europe/Oslo Europe/Paris
Europe/Podgorica Europe/Prague Europe/Riga Europe/Rome Europe/Samara
Europe/San_Marino Europe/Sarajevo Europe/Saratov Europe/Simferopol Europe/Skopje
Europe/Sofia Europe/Stockholm Europe/Tallinn Europe/Tirane Europe/Ulyanovsk
Europe/Vaduz Europe/Vatican Europe/Vienna Europe/Vilnius Europe/Volgograd
Europe/Warsaw Europe/Zagreb Europe/Zurich
Indian/Antananarivo Indian/Chagos Indian/Christmas Indian/Cocos Indian/Comoro
Indian/Kerguelen Indian/Mahe Indian/Maldives Indian/Mauritius Indian/Mayotte
Indian/Reunion
Pacific/Apia Pacific/Auckland Pacific/Bougainville Pacific/Chatham Pacific/Chuuk
Pacific/Easter Pacific/Efate Pacific/Fakaofo Pacific/Fiji Pacific/Funafuti
Pacific/Galapagos Pacific/Gambier Pacific/Guadalcanal Pacific/Guam
Pacific/Honolulu Pacific/Kanton Pacific/Kiritimati Pacific/Kosrae
Pacific/Kwajalein Pacific/Majuro Pacific/Marquesas Pacific/Midway Pacific/Nauru
Pacific/Niue Pacific/Norfolk Pacific/Noumea Pacific/Pago_Pago Pacific/Palau
Pacific/Pitcairn Pacific/Pohnpei Pacific/Port_Moresby Pacific/Rarotonga
Pacific/Saipan Pacific/Tahiti Pacific/Tarawa Pacific/Tongatapu Pacific/Wake
Pacific/Wallis`)
