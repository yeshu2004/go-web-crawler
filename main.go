package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// first we have to get the seed url. ie a array of url'
// then we have to GET req the links
// following, hash the HTML file and check with bloomfilter.
// if no, then download this HTML file into DB

type UrlSeed struct {
	Urls []string
}

func initalUrlSeed() []string {
	return []string{
		// "https://en.wikipedia.org/wiki/Ramayana",
		"https://en.wikipedia.org/wiki/Jai_Shri_Ram",
	}
}

func hashHTML(b []byte) (string, error) {
	h := sha256.New()
	_, err := h.Write(b)
	if err != nil {
		return "", err
	}
	hashSum := h.Sum(nil)                  // []bytes
	hexHash := hex.EncodeToString(hashSum) // returns hash string

	return hexHash, nil
}

func main() {
	urls := initalUrlSeed() // outputs an array for crawl
	log.Printf("total seeds for crawl (%v)\n", len(urls))

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for _, seed := range urls {
		baseURL, _ := url.Parse(seed)
		// every link will have its own queue(maybe chan what i am thinking of)

		req, err := http.NewRequest("GET", seed, nil)
		req.Header.Set("User-Agent", "MyCollageProjectCrawler (https://github.com/yourname/my-crawler; yourname@example.com)")
		if err != nil {
			log.Fatalln(err)
		}

		// GET Req
		res, err := client.Do(req)
		if err != nil {
			log.Printf("Error fetching %s: %v\n", seed, err)
			break
		}
		log.Printf("Crawling: %s\n", seed)

		b, _ := io.ReadAll(res.Body)
		defer res.Body.Close()

		// TODO: HASH
		str, err := hashHTML(b)
		if err != nil || len(str) == 0 {
			log.Fatalf("error in hashing HTML file: %v\n", err)
		}
		// TODO: BLOOMFILTER

		// TODO: IF BF SAYS NO, STORE IN DB

		// PARSING THE HTML FILE AND GETTING FURTHER LINKS AND JAI SHREE RAM
		doc, err := html.Parse(bytes.NewReader(b))
		if err != nil {
			log.Printf("parse error %s: %v", seed, err)
		}

		var linksFound []string
		var f func(*html.Node)
		f = func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "a" {
				for _, a := range n.Attr {
					if a.Key == "href" {
						fullURL := resolveURL(a.Val, baseURL)
						if fullURL != "" && strings.Contains(fullURL, "wikipedia.org") {
							linksFound = append(linksFound, fullURL)
						}
						break
					}
				}
			}
			// recurse on all children
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				f(c)
			}
		}
		f(doc)
		log.Printf("found %d links on %s\n", len(linksFound), seed)


		for _,link := range linksFound{
			fmt.Println(link)
		}
	}
}

func resolveURL(href string, base *url.URL) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(u)
	if resolved.Scheme != "https" && resolved.Scheme != "http" {
		return ""
	}
	// Remove fragments
	resolved.Fragment = ""
	return resolved.String()
}
