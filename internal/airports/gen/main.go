// Command gen refreshes airports.csv from OurAirports (public domain, https://ourairports.com/data/).
// It keeps every open airport with an IATA code. Run it with: go generate ./internal/airports
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"time"
)

const source = "https://davidmegginson.github.io/ourairports-data/airports.csv"

var iataRe = regexp.MustCompile(`^[A-Z]{3}$`)

// kinds maps OurAirports types to a short size class used to rank suggestions.
var kinds = map[string]string{
	"large_airport": "L", "medium_airport": "M", "small_airport": "S", "seaplane_base": "W", "heliport": "H",
}

func main() {
	src := flag.String("src", source, "URL or local path of the OurAirports airports.csv")
	out := flag.String("out", "airports.csv", "file to write")
	flag.Parse()
	if err := run(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(src, out string) error {
	in, err := open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	r := csv.NewReader(in)
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[name] = i
	}
	for _, name := range []string{"type", "name", "iso_country", "municipality", "scheduled_service", "iata_code"} {
		if _, ok := col[name]; !ok {
			return fmt.Errorf("column %q missing: the source format changed", name)
		}
	}

	var rows [][]string
	seen := map[string]bool{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		code, kind := rec[col["iata_code"]], kinds[rec[col["type"]]]
		if !iataRe.MatchString(code) || kind == "" || seen[code] {
			continue
		}
		seen[code] = true
		scheduled := "0"
		if rec[col["scheduled_service"]] == "yes" {
			scheduled = "1"
		}
		rows = append(rows, []string{code, rec[col["name"]], rec[col["municipality"]], rec[col["iso_country"]], kind, scheduled})
	}
	if len(rows) < 5000 {
		return fmt.Errorf("only %d airports found: refusing to overwrite %s", len(rows), out)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"iata", "name", "city", "country", "kind", "scheduled"})
	w.WriteAll(rows)
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("%s: %d airports\n", out, len(rows))
	return nil
}

func open(src string) (io.ReadCloser, error) {
	if !regexp.MustCompile(`^https?://`).MatchString(src) {
		return os.Open(src)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(src)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: status %d", src, resp.StatusCode)
	}
	return resp.Body, nil
}
