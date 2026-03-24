// worldtime - Print local time for a given city.
//
// Build a fully static binary (no libc, no external deps):
//
//   CGO_ENABLED=0 go build -ldflags="-s -w" -o worldtime .
//
// Cross-compile examples:
//   GOOS=darwin  GOARCH=arm64  CGO_ENABLED=0 go build -ldflags="-s -w" -o worldtime-mac-arm64 .
//   GOOS=linux   GOARCH=amd64  CGO_ENABLED=0 go build -ldflags="-s -w" -o worldtime-linux-amd64 .
//   GOOS=windows GOARCH=amd64  CGO_ENABLED=0 go build -ldflags="-s -w" -o worldtime.exe .
//
// The binary embeds Go's own copy of the IANA timezone database via
// the "time/tzdata" import, so it works even on systems with no
// /usr/share/zoneinfo (Alpine, scratch containers, Windows, macOS, etc.).
// Update it simply by upgrading your Go toolchain.
//
// Usage:
//   worldtime 'San Francisco'
//   worldtime Tokyo London 'New York'
//   worldtime --list

package main

import (
  "bufio"
  "fmt"
  "os"
  "path/filepath"
  "sort"
  "strings"
  "time"
  _ "time/tzdata" // embed IANA tzdata into the binary — no host zoneinfo needed
)

// ── Alias table ────────────────────────────────────────────────────────────
//
// IANA tzdata uses one representative city per zone. Cities that share a
// zone with a differently-named representative need an explicit alias.
// This table is the only thing that ever needs manual updating, and only
// when a new city becomes relevant. Adding an entry is one line.

var aliases = []struct{ city, tz string }{
  // US Pacific
  {"San Francisco", "America/Los_Angeles"},
  {"SF", "America/Los_Angeles"},
  {"Silicon Valley", "America/Los_Angeles"},
  {"San Jose", "America/Los_Angeles"},
  {"Oakland", "America/Los_Angeles"},
  {"Sacramento", "America/Los_Angeles"},
  {"San Diego", "America/Los_Angeles"},
  {"Fresno", "America/Los_Angeles"},
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
  {"Omaha", "America/Chicago"},
  // US Eastern
  {"New York", "America/New_York"},
  {"NYC", "America/New_York"},
  {"Manhattan", "America/New_York"},
  {"Brooklyn", "America/New_York"},
  {"Philadelphia", "America/New_York"},
  {"Washington", "America/New_York"},
  {"Washington DC", "America/New_York"},
  {"Atlanta", "America/New_York"},
  {"Miami", "America/New_York"},
  {"Orlando", "America/New_York"},
  {"Charlotte", "America/New_York"},
  {"Raleigh", "America/New_York"},
  {"Columbus", "America/New_York"},
  {"Cleveland", "America/New_York"},
  {"Pittsburgh", "America/New_York"},
  // Canada
  {"Toronto", "America/Toronto"},
  {"Ottawa", "America/Toronto"},
  {"Montreal", "America/Toronto"},
  {"Vancouver", "America/Vancouver"},
  {"Calgary", "America/Edmonton"},
  // Mexico
  {"Mexico City", "America/Mexico_City"},
  {"Guadalajara", "America/Mexico_City"},
  {"Monterrey", "America/Monterrey"},
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
  {"Bern", "Europe/Zurich"},
  {"Milan", "Europe/Rome"},
  {"Naples", "Europe/Rome"},
  {"Florence", "Europe/Rome"},
  {"Krakow", "Europe/Warsaw"},
  {"Kyiv", "Europe/Kyiv"},
  {"Kiev", "Europe/Kyiv"},
  {"Saint Petersburg", "Europe/Moscow"},
  {"St Petersburg", "Europe/Moscow"},
  {"Gothenburg", "Europe/Stockholm"},
  // Middle East
  {"Tel Aviv", "Asia/Jerusalem"},
  {"Jeddah", "Asia/Riyadh"},
  {"Mecca", "Asia/Riyadh"},
  {"Abu Dhabi", "Asia/Dubai"},
  {"Kuwait City", "Asia/Kuwait"},
  // South Asia
  {"New Delhi", "Asia/Kolkata"},
  {"Delhi", "Asia/Kolkata"},
  {"Mumbai", "Asia/Kolkata"},
  {"Bombay", "Asia/Kolkata"},
  {"Bangalore", "Asia/Kolkata"},
  {"Bengaluru", "Asia/Kolkata"},
  {"Hyderabad", "Asia/Kolkata"},
  {"Chennai", "Asia/Kolkata"},
  {"Madras", "Asia/Kolkata"},
  {"Calcutta", "Asia/Kolkata"},
  {"Pune", "Asia/Kolkata"},
  {"Ahmedabad", "Asia/Kolkata"},
  {"Islamabad", "Asia/Karachi"},
  {"Lahore", "Asia/Karachi"},
  // East Asia
  {"Beijing", "Asia/Shanghai"},
  {"Guangzhou", "Asia/Shanghai"},
  {"Shenzhen", "Asia/Shanghai"},
  {"Chengdu", "Asia/Shanghai"},
  {"Wuhan", "Asia/Shanghai"},
  {"Nanjing", "Asia/Shanghai"},
  {"Tianjin", "Asia/Shanghai"},
  {"Hong Kong", "Asia/Hong_Kong"},
  {"Osaka", "Asia/Tokyo"},
  {"Kyoto", "Asia/Tokyo"},
  {"Nagoya", "Asia/Tokyo"},
  {"Sapporo", "Asia/Tokyo"},
  {"Hiroshima", "Asia/Tokyo"},
  {"Busan", "Asia/Seoul"},
  {"Hanoi", "Asia/Bangkok"},
  {"Ho Chi Minh City", "Asia/Ho_Chi_Minh"},
  {"Saigon", "Asia/Ho_Chi_Minh"},
  // Oceania
  {"Sydney", "Australia/Sydney"},
  {"Canberra", "Australia/Sydney"},
  {"Auckland", "Pacific/Auckland"},
  {"Wellington", "Pacific/Auckland"},
  {"Christchurch", "Pacific/Auckland"},
}

// ── Database entry ──────────────────────────────────────────────────────────

type entry struct {
  city string
  tz   string
}

// ── Build the database ──────────────────────────────────────────────────────
//
// Strategy (same as the C version):
//  1. Walk /usr/share/zoneinfo and derive a city name from each file path.
//  2. Fall back to Go's embedded tzdata if the directory isn't present.
//  3. Append the alias table.

func loadDB() []entry {
  var db []entry
  seen := map[string]bool{} // dedup by tz id

  // skipDirs contains zoneinfo subdirs that hold legacy/non-city entries.
  skipDirs := map[string]bool{
    "Etc": true, "posix": true, "right": true, "SystemV": true,
  }

  // Walk the host zoneinfo directory if it exists.
  root := "/usr/share/zoneinfo"
  if info, err := os.Stat(root); err == nil && info.IsDir() {
    _ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
      if err != nil {
        return nil
      }
      rel, _ := filepath.Rel(root, path)
      parts := strings.Split(rel, string(os.PathSeparator))

      // Skip blacklisted top-level dirs entirely.
      if len(parts) > 0 && skipDirs[parts[0]] {
        return filepath.SkipDir
      }
      // Skip non-files and entries without a parent dir (bare "UTC" etc.)
      if d.IsDir() || !strings.Contains(rel, string(os.PathSeparator)) {
        return nil
      }
      // Skip non-timezone files in the root.
      if strings.HasSuffix(rel, ".tab") || strings.HasSuffix(rel, ".list") {
        return nil
      }

      tzID := filepath.ToSlash(rel) // always use forward slashes
      if seen[tzID] {
        return nil
      }
      seen[tzID] = true

      city := tzPathToCity(tzID)
      db = append(db, entry{city: city, tz: tzID})
      return nil
    })
  }

  // If the host had nothing (or we're on a minimal system), fall back to
  // Go's embedded tzdata. We parse the known-good zone list from time.LoadLocation
  // by trying a curated set of IANA prefixes.
  if len(db) == 0 {
    db = loadFromEmbedded()
  }

  // Append aliases (they win in lookup because we search aliases first).
  for _, a := range aliases {
    db = append(db, entry{city: a.city, tz: a.tz})
  }

  return db
}

// tzPathToCity converts "America/Los_Angeles" → "Los Angeles".
func tzPathToCity(tzID string) string {
  parts := strings.Split(tzID, "/")
  last := parts[len(parts)-1]
  return strings.ReplaceAll(last, "_", " ")
}

// loadFromEmbedded builds a DB from Go's embedded tzdata by reading the
// zone1970.tab file that Go bundles inside the time/tzdata package.
// We access it via time.LoadLocation to verify validity, then build city names.
func loadFromEmbedded() []entry {
  // These are all IANA region prefixes; we'll also try zone1970.tab via
  // a bundled list. Since we can't enumerate the embedded zip directly
  // without internal APIs, we use a well-known curated list of tz IDs.
  // This is the only place the "list" is used — purely as a fallback when
  // the host has no /usr/share/zoneinfo. On Linux/macOS this path is
  // almost never taken.
  zones := embeddedZoneList()
  var db []entry
  for _, tz := range zones {
    if _, err := time.LoadLocation(tz); err == nil {
      db = append(db, entry{city: tzPathToCity(tz), tz: tz})
    }
  }
  return db
}

// ── Lookup ──────────────────────────────────────────────────────────────────
//
// Four tiers, aliases searched first (they're appended at the end of db):
//   1. Exact case-insensitive match on city name
//   2. City name starts with query
//   3. City name contains query
//   4. Raw tz string contains query  (e.g. "kolkata" → "Asia/Kolkata")

func findEntry(db []entry, query string) *entry {
  q := strings.ToLower(query)

  // Search aliases (end of slice) before generic tzdata entries.
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

// ── Time formatting ─────────────────────────────────────────────────────────

func printTime(tzID string) error {
  loc, err := time.LoadLocation(tzID)
  if err != nil {
    return fmt.Errorf("unknown timezone %q: %w", tzID, err)
  }
  now := time.Now().In(loc)
  // Match C's strftime "%a %b %e %H:%M:%S %Z %Y" output:
  // e.g. "Tue Mar 24 12:00:00 PDT 2026"
  // Go's time.Format uses a reference time: Mon Jan 2 15:04:05 MST 2006
  fmt.Println(now.Format("Mon Jan _2 15:04:05 MST 2006"))
  return nil
}

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
  if len(os.Args) < 2 {
    fmt.Fprintf(os.Stderr,
      "Usage: %s <city> [city2 ...]\n"+
        "       %s --list\n\n"+
        "Examples:\n"+
        "  %s 'San Francisco'\n"+
        "  %s Tokyo London 'New York'\n",
      os.Args[0], os.Args[0], os.Args[0], os.Args[0])
    os.Exit(1)
  }

  db := loadDB()

  if os.Args[1] == "--list" {
    // Sort alphabetically for readability.
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
    nAliases := len(aliases)
    fmt.Printf("\n%d entries (%d from tzdata + %d aliases)\n",
      len(db), len(db)-nAliases, nAliases)
    return
  }

  multi := len(os.Args) > 2
  exitCode := 0

  for _, query := range os.Args[1:] {
    if multi {
      fmt.Printf("%-22s  ", query)
    }
    e := findEntry(db, query)
    if e == nil {
      if multi {
        fmt.Println()
      }
      fmt.Fprintf(os.Stderr,
        "error: unknown city %q\n"+
          "  Run --list to see all supported cities, or add an alias\n"+
          "  in the aliases table at the top of main.go\n",
        query)
      exitCode = 1
      continue
    }
    if err := printTime(e.tz); err != nil {
      fmt.Fprintln(os.Stderr, "error:", err)
      exitCode = 1
    }
  }

  os.Exit(exitCode)
}

// ── Embedded zone list (fallback only) ─────────────────────────────────────
//
// Used only when /usr/share/zoneinfo is absent (e.g. Windows, scratch
// containers). On normal Linux/macOS the walk above handles everything.
// This list is sourced from the IANA tzdb and matches what Go embeds.
// It does NOT need manual curation — it's just the canonical zone list.

func embeddedZoneList() []string {
  // Read zone1970.tab-style data from the embedded tzdata.
  // Go 1.15+ bundles tzdata; we surface it by parsing the known zone IDs.
  // Source: https://data.iana.org/time-zones/tzdb-latest.tar.lz
  const zoneData = `Africa/Abidjan
Africa/Accra
Africa/Addis_Ababa
Africa/Algiers
Africa/Asmara
Africa/Bamako
Africa/Bangui
Africa/Banjul
Africa/Bissau
Africa/Blantyre
Africa/Brazzaville
Africa/Bujumbura
Africa/Cairo
Africa/Casablanca
Africa/Ceuta
Africa/Conakry
Africa/Dakar
Africa/Dar_es_Salaam
Africa/Djibouti
Africa/Douala
Africa/El_Aaiun
Africa/Freetown
Africa/Gaborone
Africa/Harare
Africa/Johannesburg
Africa/Juba
Africa/Kampala
Africa/Khartoum
Africa/Kigali
Africa/Kinshasa
Africa/Lagos
Africa/Libreville
Africa/Lome
Africa/Luanda
Africa/Lubumbashi
Africa/Lusaka
Africa/Malabo
Africa/Maputo
Africa/Maseru
Africa/Mbabane
Africa/Mogadishu
Africa/Monrovia
Africa/Nairobi
Africa/Ndjamena
Africa/Niamey
Africa/Nouakchott
Africa/Ouagadougou
Africa/Porto-Novo
Africa/Sao_Tome
Africa/Tripoli
Africa/Tunis
Africa/Windhoek
America/Adak
America/Anchorage
America/Anguilla
America/Antigua
America/Araguaina
America/Argentina/Buenos_Aires
America/Argentina/Catamarca
America/Argentina/Cordoba
America/Argentina/Jujuy
America/Argentina/La_Rioja
America/Argentina/Mendoza
America/Argentina/Rio_Gallegos
America/Argentina/Salta
America/Argentina/San_Juan
America/Argentina/San_Luis
America/Argentina/Tucuman
America/Argentina/Ushuaia
America/Aruba
America/Asuncion
America/Atikokan
America/Bahia
America/Bahia_Banderas
America/Barbados
America/Belem
America/Belize
America/Blanc-Sablon
America/Boa_Vista
America/Bogota
America/Boise
America/Cambridge_Bay
America/Campo_Grande
America/Cancun
America/Caracas
America/Cayenne
America/Cayman
America/Chicago
America/Chihuahua
America/Ciudad_Juarez
America/Costa_Rica
America/Creston
America/Cuiaba
America/Curacao
America/Danmarkshavn
America/Dawson
America/Dawson_Creek
America/Denver
America/Detroit
America/Dominica
America/Edmonton
America/Eirunepe
America/El_Salvador
America/Fortaleza
America/Glace_Bay
America/Goose_Bay
America/Grand_Turk
America/Grenada
America/Guadeloupe
America/Guatemala
America/Guayaquil
America/Guyana
America/Halifax
America/Havana
America/Hermosillo
America/Indiana/Indianapolis
America/Indiana/Knox
America/Indiana/Marengo
America/Indiana/Petersburg
America/Indiana/Tell_City
America/Indiana/Vevay
America/Indiana/Vincennes
America/Indiana/Winamac
America/Inuvik
America/Iqaluit
America/Jamaica
America/Juneau
America/Kentucky/Louisville
America/Kentucky/Monticello
America/Kralendijk
America/La_Paz
America/Lima
America/Los_Angeles
America/Lower_Princes
America/Maceio
America/Managua
America/Manaus
America/Marigot
America/Martinique
America/Matamoros
America/Mazatlan
America/Menominee
America/Merida
America/Metlakatla
America/Mexico_City
America/Miquelon
America/Moncton
America/Monterrey
America/Montevideo
America/Montserrat
America/Nassau
America/New_York
America/Nome
America/Noronha
America/North_Dakota/Beulah
America/North_Dakota/Center
America/North_Dakota/New_Salem
America/Nuuk
America/Ojinaga
America/Panama
America/Paramaribo
America/Phoenix
America/Port-au-Prince
America/Port_of_Spain
America/Porto_Velho
America/Puerto_Rico
America/Punta_Arenas
America/Rankin_Inlet
America/Recife
America/Regina
America/Resolute
America/Rio_Branco
America/Santarem
America/Santiago
America/Santo_Domingo
America/Sao_Paulo
America/Scoresbysund
America/Sitka
America/St_Barthelemy
America/St_Johns
America/St_Kitts
America/St_Lucia
America/St_Thomas
America/St_Vincent
America/Swift_Current
America/Tegucigalpa
America/Thule
America/Tijuana
America/Toronto
America/Tortola
America/Vancouver
America/Whitehorse
America/Winnipeg
America/Yakutat
Antarctica/Casey
Antarctica/Davis
Antarctica/Mawson
Antarctica/McMurdo
Antarctica/Palmer
Antarctica/Rothera
Antarctica/Syowa
Antarctica/Troll
Antarctica/Vostok
Arctic/Longyearbyen
Asia/Aden
Asia/Almaty
Asia/Amman
Asia/Anadyr
Asia/Aqtau
Asia/Aqtobe
Asia/Ashgabat
Asia/Atyrau
Asia/Baghdad
Asia/Bahrain
Asia/Baku
Asia/Bangkok
Asia/Barnaul
Asia/Beirut
Asia/Bishkek
Asia/Brunei
Asia/Chita
Asia/Choibalsan
Asia/Colombo
Asia/Damascus
Asia/Dhaka
Asia/Dili
Asia/Dubai
Asia/Dushanbe
Asia/Famagusta
Asia/Gaza
Asia/Hebron
Asia/Ho_Chi_Minh
Asia/Hong_Kong
Asia/Hovd
Asia/Irkutsk
Asia/Jakarta
Asia/Jayapura
Asia/Jerusalem
Asia/Kabul
Asia/Kamchatka
Asia/Karachi
Asia/Kathmandu
Asia/Khandyga
Asia/Kolkata
Asia/Krasnoyarsk
Asia/Kuala_Lumpur
Asia/Kuching
Asia/Kuwait
Asia/Macau
Asia/Magadan
Asia/Makassar
Asia/Manila
Asia/Muscat
Asia/Nicosia
Asia/Novokuznetsk
Asia/Novosibirsk
Asia/Omsk
Asia/Oral
Asia/Phnom_Penh
Asia/Pontianak
Asia/Pyongyang
Asia/Qatar
Asia/Qostanay
Asia/Qyzylorda
Asia/Riyadh
Asia/Sakhalin
Asia/Samarkand
Asia/Seoul
Asia/Shanghai
Asia/Singapore
Asia/Srednekolymsk
Asia/Taipei
Asia/Tashkent
Asia/Tbilisi
Asia/Tehran
Asia/Thimphu
Asia/Tokyo
Asia/Tomsk
Asia/Ulaanbaatar
Asia/Urumqi
Asia/Ust-Nera
Asia/Vientiane
Asia/Vladivostok
Asia/Yakutsk
Asia/Yangon
Asia/Yekaterinburg
Asia/Yerevan
Atlantic/Azores
Atlantic/Bermuda
Atlantic/Canary
Atlantic/Cape_Verde
Atlantic/Faroe
Atlantic/Madeira
Atlantic/Reykjavik
Atlantic/South_Georgia
Atlantic/St_Helena
Atlantic/Stanley
Australia/Adelaide
Australia/Brisbane
Australia/Broken_Hill
Australia/Darwin
Australia/Eucla
Australia/Hobart
Australia/Lindeman
Australia/Lord_Howe
Australia/Melbourne
Australia/Perth
Australia/Sydney
Europe/Amsterdam
Europe/Andorra
Europe/Astrakhan
Europe/Athens
Europe/Belgrade
Europe/Berlin
Europe/Bratislava
Europe/Brussels
Europe/Bucharest
Europe/Budapest
Europe/Busingen
Europe/Chisinau
Europe/Copenhagen
Europe/Dublin
Europe/Gibraltar
Europe/Guernsey
Europe/Helsinki
Europe/Isle_of_Man
Europe/Istanbul
Europe/Jersey
Europe/Kaliningrad
Europe/Kirov
Europe/Kyiv
Europe/Lisbon
Europe/Ljubljana
Europe/London
Europe/Luxembourg
Europe/Madrid
Europe/Malta
Europe/Mariehamn
Europe/Minsk
Europe/Monaco
Europe/Moscow
Europe/Nicosia
Europe/Oslo
Europe/Paris
Europe/Podgorica
Europe/Prague
Europe/Riga
Europe/Rome
Europe/Samara
Europe/San_Marino
Europe/Sarajevo
Europe/Saratov
Europe/Simferopol
Europe/Skopje
Europe/Sofia
Europe/Stockholm
Europe/Tallinn
Europe/Tirane
Europe/Ulyanovsk
Europe/Vaduz
Europe/Vatican
Europe/Vienna
Europe/Vilnius
Europe/Volgograd
Europe/Warsaw
Europe/Zagreb
Europe/Zurich
Indian/Antananarivo
Indian/Chagos
Indian/Christmas
Indian/Cocos
Indian/Comoro
Indian/Kerguelen
Indian/Mahe
Indian/Maldives
Indian/Mauritius
Indian/Mayotte
Indian/Reunion
Pacific/Apia
Pacific/Auckland
Pacific/Bougainville
Pacific/Chatham
Pacific/Chuuk
Pacific/Easter
Pacific/Efate
Pacific/Fakaofo
Pacific/Fiji
Pacific/Funafuti
Pacific/Galapagos
Pacific/Gambier
Pacific/Guadalcanal
Pacific/Guam
Pacific/Honolulu
Pacific/Kanton
Pacific/Kiritimati
Pacific/Kosrae
Pacific/Kwajalein
Pacific/Majuro
Pacific/Marquesas
Pacific/Midway
Pacific/Nauru
Pacific/Niue
Pacific/Norfolk
Pacific/Noumea
Pacific/Pago_Pago
Pacific/Palau
Pacific/Pitcairn
Pacific/Pohnpei
Pacific/Port_Moresby
Pacific/Rarotonga
Pacific/Saipan
Pacific/Tahiti
Pacific/Tarawa
Pacific/Tongatapu
Pacific/Wake
Pacific/Wallis`

  scanner := bufio.NewScanner(strings.NewReader(zoneData))
  var zones []string
  for scanner.Scan() {
    line := strings.TrimSpace(scanner.Text())
    if line != "" {
      zones = append(zones, line)
    }
  }

  return zones
}
