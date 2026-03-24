// worldtime - Print local time for any city in the world.
//
// City resolution works in two stages:
//
//  1. Fast local lookup: city name matched against IANA tzdata zone names
//     + a small alias table. Handles the common cases instantly, offline.
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
	"path/filepath"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // embed IANA tzdata — no host zoneinfo needed

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

func loadDB() []entry {
	var db []entry
	seen := map[string]bool{}

	skipDirs := map[string]bool{
		"Etc": true, "posix": true, "right": true, "SystemV": true,
	}

	root := "/usr/share/zoneinfo"
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			parts := strings.Split(rel, string(os.PathSeparator))
			if len(parts) > 0 && skipDirs[parts[0]] {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.Contains(rel, string(os.PathSeparator)) {
				return nil
			}
			if strings.HasSuffix(rel, ".tab") || strings.HasSuffix(rel, ".list") {
				return nil
			}
			tzID := filepath.ToSlash(rel)
			if seen[tzID] {
				return nil
			}
			seen[tzID] = true
			parts2 := strings.Split(tzID, "/")
			city := strings.ReplaceAll(parts2[len(parts2)-1], "_", " ")
			db = append(db, entry{city: city, tz: tzID})
			return nil
		})
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
	Lat string `json:"lat"`
	Lon string `json:"lon"`
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
