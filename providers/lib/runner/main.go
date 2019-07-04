// Copyright (c) Facebook, Inc. and its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runner

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	nvd "github.com/facebookincubator/nvdtools/cvefeed/jsonschema"
)

// Convertible is any struct which knows how to convert itself to NVD CVE Item
type Convertible interface {
	// ID should return vulnerabilities ID
	ID() string
	// Convert should return a new CVE Item, or an error if it's not possible
	Convert() (*nvd.NVDCVEFeedJSON10DefCVEItem, error)
}

// Read should read the vulnerabilities from the given reader and push them into the channel
// The contents of the reader should be a slice of structs which are convertibles
// channel will be created and mustn't be closed
type Read func(io.Reader, chan Convertible) error

// FetchSince knows how to fetch vulnerabilities from an API
// it should create a new channel, fetch everything concurrently and close the channel
type FetchSince func(baseURL, userAgent string, since int64) (<-chan Convertible, error)

// Runner knows how to run everything together, based on the config values
// if config.Download is set, it will use the fetcher, otherwise it will use Reader to read stdin or files
type Runner struct {
	Config
	FetchSince
	Read
}

// Run should be called in main function of the converter
// It will run the fetchers/runners (and convert vulnerabilities)
// Finally, it will output it as json to stdout
func (r *Runner) Run() error {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	r.Config.addFlags()
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}
	flag.Parse()

	if err := r.Config.validate(); err != nil {
		return fmt.Errorf("config is invalid: %v", err)
	}

	vulns, err := r.getVulnerabilities()
	if err != nil {
		return fmt.Errorf("couldn't get vulnerabilities: %v", err)
	}

	if r.Config.convert {
		feed := getNVDFeed(vulns)
		if err := json.NewEncoder(os.Stdout).Encode(feed); err != nil {
			return fmt.Errorf("couldn't write NVD feed: %v", err)
		}
		return nil
	}

	m := make(map[string]Convertible)
	for v := range vulns {
		m[v.ID()] = v
	}
	if err := json.NewEncoder(os.Stdout).Encode(m); err != nil {
		return fmt.Errorf("couldn't write vulnerabilities: %v", err)
	}

	return nil
}

// getVulnerabilities will either get vulnerabilities using fetcher (if download is set) or stdin/files
func (r *Runner) getVulnerabilities() (<-chan Convertible, error) {

	if r.Config.download {
		// fetch vulnerabilites since provided timestamp
		return r.FetchSince(r.Config.BaseURL, r.Config.UserAgent, int64(r.Config.downloadSince))
	}

	if flag.NArg() == 0 {
		// read from stdin
		vulns := make(chan Convertible)
		go func() {
			defer close(vulns)
			if err := r.Read(os.Stdin, vulns); err != nil {
				log.Printf("error while reading from stdin: %v", err)
			}
		}()
		return vulns, nil
	}

	// read from files in args
	vulns := make(chan Convertible)
	wg := sync.WaitGroup{}
	for _, filename := range flag.Args() {
		wg.Add(1)
		go func(filename string) {
			defer wg.Done()
			file, err := os.Open(filename)
			if err != nil {
				log.Printf("couldn't open file %q: %v", filename, err)
				return
			}
			defer file.Close()
			if err := r.Read(file, vulns); err != nil {
				log.Printf("error while reading from file %q: %v", filename, err)
			}
		}(filename)
	}
	go func() {
		defer close(vulns)
		wg.Wait()
	}()

	return vulns, nil
}

// getNVDFeed will convert the vulns in channel to NVD Feed
func getNVDFeed(vulns <-chan Convertible) *nvd.NVDCVEFeedJSON10 {
	var feed nvd.NVDCVEFeedJSON10
	for vuln := range vulns {
		converted, err := vuln.Convert()
		if err != nil {
			log.Printf("error while converting vuln: %v", err)
			continue
		}
		feed.CVEItems = append(feed.CVEItems, converted)
	}
	return &feed
}