// SPDX-License-Identifier: 0BSD

package app

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

const siteBase = "https://www.moddb.com"

// Mod is the metadata scraped from a mod profile page.
type Mod struct {
	Slug        string `json:"slug"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Game        string `json:"game"`
	GameURL     string `json:"game_url"`
	HeaderImage string `json:"header_image"`
	ProfileHTML string `json:"-"`
}

// FileStub is one row of the downloads list.
type FileStub struct {
	PageURL  string `json:"page_url"`
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Added    string `json:"added"`
	Summary  string `json:"summary"`
	Thumb    string `json:"thumb"`
}

// FileInfo is the detail block of a single download page.
type FileInfo struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Category  string `json:"category"`
	Uploader  string `json:"uploader"`
	Added     string `json:"added"`
	Size      int64  `json:"size"`
	Downloads string `json:"downloads"`
	MD5       string `json:"md5"`
	StartURL  string `json:"start_url"`
}

var (
	reH1           = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
	reMetaDesc     = regexp.MustCompile(`(?s)<meta name="description" content="([^"]*)"`)
	reAuthor       = regexp.MustCompile(`(?s)itemprop="author"[^>]*>.*?itemprop="name">([^<]+)`)
	reGame         = regexp.MustCompile(`"@id":"https://www\.moddb\.com/games/([^"]+)","name":"([^"]+)"`)
	reHeaderImg    = regexp.MustCompile(`headerbox"[^>]*url\(([^)]+)\)`)
	reH5Row        = regexp.MustCompile(`(?s)<h5>\s*([^<]+?)\s*</h5>\s*<span class="summary">\s*(.*?)\s*</span>`)
	reTime         = regexp.MustCompile(`<time datetime="([^"]+)"`)
	reStart        = regexp.MustCompile(`(?:href="|src=")(?:https://www\.moddb\.com)?/downloads/start/(\d+)`)
	reFileID       = regexp.MustCompile(`/(?:statistics/data/visit/downloads|download/medium)/(\d+)`)
	reMirror       = regexp.MustCompile(`(?:href="|location\.href=")(/downloads/mirror/[^"]+)`)
	reFileRowStart = regexp.MustCompile(`<div class="row rowcontent[^"]*">`)
	reFileLink     = regexp.MustCompile(`href="(/mods/[^"]+/downloads/[a-zA-Z0-9-]+)"(?:\s+title="([^"]*)")?`)
	reH4           = regexp.MustCompile(`(?s)<h4><a [^>]*>(.*?)</a></h4>`)
	reSubhead      = regexp.MustCompile(`(?s)<span class="subheading">\s*(.*?)\s*</span>`)
	reRowP         = regexp.MustCompile(`(?s)<p>(.*?)</p>`)
	reThumb        = regexp.MustCompile(`<img src="(https://media\.moddb\.com/cache/images/downloads/[^"]+)"`)
	rePageNum      = regexp.MustCompile(`href="[^"]*/(downloads|images|videos)/page/(\d+)[^"]*"`)
	reFullImg      = regexp.MustCompile(`(?:href|src)="(https://media\.moddb\.com/images/[^"]+\.(?:png|jpe?g|gif|webp))"`)
	reVideo        = regexp.MustCompile(`(?:href|src)="(https://media\.moddb\.com/[^"]+\.(?:mp4|webm|mov|wmv|avi))"`)
	reSize         = regexp.MustCompile(`\(([\d,]+) bytes\)`)
	reTags         = regexp.MustCompile(`<[^>]+>`)
)

func stripTags(s string) string {
	s = reTags.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

// matchDiv finds the end of the div that opens at pos and returns the
// inner markup. Pos must point at an opening "<div" tag.
func matchDiv(s string, pos int) string {
	if pos < 0 || pos+4 > len(s) || s[pos:pos+4] != "<div" {
		return ""
	}
	depth := 1
	i := pos + 4
	for i < len(s) {
		open := strings.Index(s[i:], "<div")
		close_ := strings.Index(s[i:], "</div>")
		if close_ == -1 {
			return ""
		}
		if open != -1 && open < close_ {
			depth++
			i += open + 4
			continue
		}
		depth--
		if depth == 0 {
			// content between the first '>' and the closing tag
			gt := strings.IndexByte(s[pos:], '>')
			if gt == -1 {
				return ""
			}
			return s[pos+gt+1 : i+close_]
		}
		i += close_ + 6
	}
	return ""
}

func ParseModPage(body []byte, slug string) *Mod {
	s := string(body)
	m := &Mod{Slug: slug, URL: siteBase + "/mods/" + slug}
	if x := reH1.FindStringSubmatch(s); x != nil {
		m.Title = stripTags(x[1])
	}
	if x := reMetaDesc.FindStringSubmatch(s); x != nil {
		m.Description = html.UnescapeString(strings.TrimSpace(x[1]))
	}
	if x := reAuthor.FindStringSubmatch(s); x != nil {
		m.Author = html.UnescapeString(strings.TrimSpace(x[1]))
	}
	if x := reGame.FindStringSubmatch(s); x != nil {
		m.GameURL = siteBase + "/games/" + x[1]
		m.Game = html.UnescapeString(strings.TrimSpace(x[2]))
	}
	if x := reHeaderImg.FindStringSubmatch(s); x != nil {
		m.HeaderImage = x[1]
	}
	if pos := strings.Index(s, `<div id="profiledescription"`); pos >= 0 {
		m.ProfileHTML = matchDiv(s, pos)
	}
	return m
}

// ParseDownloadsList extracts file stubs from a downloads listing
// page.
func ParseDownloadsList(body []byte) []FileStub {
	s := string(body)
	var out []FileStub
	seen := map[string]bool{}
	locs := reFileRowStart.FindAllStringIndex(s, -1)
	for i, loc := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := s[loc[1]:end]
		link := reFileLink.FindStringSubmatch(chunk)
		if link == nil {
			continue
		}
		pageURL := link[1]
		if seen[pageURL] {
			continue
		}
		seen[pageURL] = true
		st := FileStub{PageURL: siteBase + pageURL}
		if i := strings.LastIndex(pageURL, "/"); i >= 0 {
			st.Slug = pageURL[i+1:]
		}
		if link[2] != "" {
			st.Title = html.UnescapeString(link[2])
		}
		if x := reH4.FindStringSubmatch(chunk); x != nil {
			st.Title = stripTags(x[1])
		}
		if x := reSubhead.FindStringSubmatch(chunk); x != nil {
			st.Category = stripTags(x[1])
		}
		if x := reTime.FindStringSubmatch(chunk); x != nil {
			st.Added = x[1]
		}
		if x := reRowP.FindStringSubmatch(chunk); x != nil {
			st.Summary = stripTags(x[1])
		}
		if x := reThumb.FindStringSubmatch(chunk); x != nil {
			st.Thumb = x[1]
		}
		out = append(out, st)
	}
	return out
}

// ParsePageNums returns the highest page number linked from a listing
// page so the caller can fetch 2..N.
func ParsePageNums(body []byte, section string) int {
	max := 0
	for _, m := range rePageNum.FindAllStringSubmatch(string(body), -1) {
		if m[1] != section {
			continue
		}
		if n, err := strconv.Atoi(m[2]); err == nil && n > max {
			max = n
		}
	}
	return max
}

// ParseFilePage reads the h5/summary table of a download page.
func ParseFilePage(body []byte) *FileInfo {
	s := string(body)
	fi := &FileInfo{}
	for _, row := range reH5Row.FindAllStringSubmatch(s, -1) {
		key := strings.TrimSpace(row[1])
		val := stripTags(row[2])
		switch key {
		case "Filename":
			fi.Filename = val
		case "Category":
			fi.Category = val
		case "Uploader":
			fi.Uploader = val
		case "Added":
			if x := reTime.FindStringSubmatch(row[2]); x != nil {
				fi.Added = x[1]
			} else {
				fi.Added = val
			}
		case "Size":
			if x := reSize.FindStringSubmatch(val); x != nil {
				n, _ := strconv.ParseInt(strings.ReplaceAll(x[1], ",", ""), 10, 64)
				fi.Size = n
			}
		case "Downloads":
			fi.Downloads = val
		case "MD5 Hash":
			fi.MD5 = val
		}
	}
	var id string
	if x := reStart.FindStringSubmatch(s); x != nil {
		fi.StartURL = siteBase + "/downloads/start/" + x[1]
		id = x[1]
	} else if x := reFileID.FindStringSubmatch(s); x != nil {
		id = x[1]
		fi.StartURL = siteBase + "/downloads/start/" + id
	}
	fi.ID = id
	return fi
}

// ParseStartPage pulls the mirror link out of a downloads/start page.
func ParseStartPage(body []byte) string {
	if x := reMirror.FindStringSubmatch(string(body)); x != nil {
		return siteBase + x[1]
	}
	return ""
}

// ParseMediaList collects original image URLs from a gallery page.
func ParseMediaList(body []byte, video bool) []string {
	s := string(body)
	re := reFullImg
	if video {
		re = reVideo
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		u := m[1]
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}
