/*
Copyright (C) 2022  Kyle Sanderson

This program is free software; you can redistribute it and/or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; specifically version 2
of the License.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program; if not, write to the Free Software
Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301, USA.
*/

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/autobrr/go-qbittorrent"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/moistari/rls"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Entry struct {
	t qbittorrent.Torrent
	r rls.Release
}

type upgradereq struct {
	Name string

	User     string
	Password string
	Host     string
	Port     uint

	Hash    string
	Torrent json.RawMessage
	Client  *qbittorrent.Client
}

type timeentry struct {
	e   map[string][]Entry
	t   time.Time
	err error
}

var clientmap sync.Map
var torrentmap sync.Map

func main() {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.URLFormat)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("k8s"))
	})

	r.Post("/api/upgrade", handleUpgrade)
	r.Post("/api/cross", handleCross)
	http.ListenAndServe(":6940", r) /* immutable. this is b's favourite positive 4digit number not starting with a 0. */
}

func getClient(req *upgradereq) error {
	s := qbittorrent.Config{
		Host:     req.Host,
		Username: req.User,
		Password: req.Password,
	}

	c, ok := clientmap.Load(s)
	if !ok {
		c = qbittorrent.NewClient(qbittorrent.Config{
			Host:     req.Host,
			Username: req.User,
			Password: req.Password,
		})

		if err := c.(*qbittorrent.Client).Login(); err != nil {
			return err
		}

		clientmap.Store(s, c)
	}

	req.Client = c.(*qbittorrent.Client)
	return nil
}

func heartbeat(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Alive", 200)
}

func (c *upgradereq) getAllTorrents() timeentry {
	set := qbittorrent.Config{
		Host:     c.Host,
		Username: c.User,
		Password: c.Password,
	}

	res, ok := torrentmap.Load(set)
	if !ok || res.(timeentry).t.Unix()+60 < time.Now().Unix() {
		torrents, err := c.Client.GetTorrents(qbittorrent.TorrentFilterOptions{})
		if err != nil {
			return timeentry{err: err}
		}

		mp := timeentry{e: make(map[string][]Entry), t: time.Now()}
		for _, t := range torrents {
			r := rls.ParseString(t.Name)
			s := getFormattedTitle(r)
			mp.e[s] = append(mp.e[s], Entry{t: t, r: r})
		}

		torrentmap.Store(set, mp)
		res = mp
	}

	return res.(timeentry)
}

func (c *upgradereq) getFiles(hash string) (*qbittorrent.TorrentFiles, error) {
	fmt.Printf("HASH: %q\n", hash)
	return c.Client.GetFilesInformation(hash)
}

func (c *upgradereq) getCategories() (map[string]qbittorrent.Category, error) {
	return c.Client.GetCategories()
}

func (c *upgradereq) createCategory(cat, savePath string) error {
	return c.Client.CreateCategory(cat, savePath)
}

func (c *upgradereq) recheckTorrent() error {
	return c.Client.Recheck(append(make([]string, 0, 1), c.Hash))
}

func (c *upgradereq) setTorrentManagement(enable bool) error {
	return c.Client.SetAutoManagement(append(make([]string, 0, 1), c.Hash), enable)
}

func (c *upgradereq) resumeTorrent() error {
	return c.Client.Resume(append(make([]string, 0, 1), c.Hash))
}

func (c *upgradereq) setLocationTorrent(location string) error {
	return c.Client.SetLocation(append(make([]string, 0, 1), c.Hash), location)
}

func (c *upgradereq) deleteTorrent() error {
	return c.Client.DeleteTorrents(append(make([]string, 0, 1), c.Hash), false)
}

func (c *upgradereq) renameFile(hash, oldPath, newPath string) error {
	return c.Client.RenameFile(hash, oldPath, newPath)
}

func (c *upgradereq) getTrackers() ([]qbittorrent.TorrentTracker, error) {
	return c.Client.GetTorrentTrackers(c.Hash)
}

func (c *upgradereq) announceTrackers() error {
	return c.Client.ReAnnounceTorrents([]string{c.Hash})
}

func (c *upgradereq) submitTorrent(opts *qbittorrent.TorrentAddOptions) error {
	f, err := os.CreateTemp("", "upgraderr-sub.")
	if err != nil {
		return fmt.Errorf("Unable to tmpfile: %q", err)
	}

	defer f.Close()
	defer os.Remove(f.Name())

	if _, err = f.Write(c.Torrent); err != nil {
		return fmt.Errorf("Unable to write (%q): %q", err, f.Name())
	}

	if err = f.Sync(); err != nil {
		return fmt.Errorf("Unable to sync (%q): %q", err, f.Name())
	}

	return c.Client.AddTorrentFromFile(f.Name(), opts.Prepare())
}

func (c *upgradereq) getTorrent() (qbittorrent.Torrent, error) {
	if len(c.Hash) != 0 {
		torrents, err := c.Client.GetTorrents(qbittorrent.TorrentFilterOptions{Hashes: append(make([]string, 0, 1), c.Hash)})
		if err != nil {
			return qbittorrent.Torrent{}, err
		} else if len(torrents) == 0 {
			return qbittorrent.Torrent{}, fmt.Errorf("Unable to find Hash: %q", c.Hash)
		}

		for _, t := range torrents {
			if t.Hash == c.Hash {
				return t, nil
			}
		}

		return qbittorrent.Torrent{}, fmt.Errorf("Unable to find Hash after lookup: %q", c.Hash)
	}

	t, err := c.Client.GetTorrents(qbittorrent.TorrentFilterOptions{Tag: "upgraderr"})
	if err != nil {
		return qbittorrent.Torrent{}, err
	}

	for _, v := range t {
		switch v.State {
		case qbittorrent.TorrentStateError, qbittorrent.TorrentStateMissingFiles,
			qbittorrent.TorrentStatePausedDl, qbittorrent.TorrentStatePausedUp,
			qbittorrent.TorrentStateCheckingDl, qbittorrent.TorrentStateCheckingUp, qbittorrent.TorrentStateCheckingResumeData:
			if c.Name == v.Name {
				return v, nil
			}
		default:
			if c.Name == v.Name {
				fmt.Printf("Found non-conforming: %q | %q\n", v.Name, v.State)
			}
		}
	}

	return qbittorrent.Torrent{}, fmt.Errorf("Unable to find %q", c.Name)
}

func handleUpgrade(w http.ResponseWriter, r *http.Request) {
	var req upgradereq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 470)
		return
	}

	if len(req.Name) == 0 {
		http.Error(w, fmt.Sprintf("No title passed.\n"), 469)
		return
	}

	if err := getClient(&req); err != nil {
		http.Error(w, fmt.Sprintf("Unable to get client: %q\n", err), 471)
		return
	}

	mp := req.getAllTorrents()
	if mp.err != nil {
		http.Error(w, fmt.Sprintf("Unable to get result: %q\n", mp.err), 468)
		return
	}

	requestrls := Entry{r: rls.ParseString(req.Name)}
	if v, ok := mp.e[getFormattedTitle(requestrls.r)]; ok {
		code := 0
		var parent Entry
		for _, child := range v {
			if rls.Compare(requestrls.r, child.r) == 0 {
				parent = child
				code = -1
				break
			}

			if res := checkResolution(&requestrls, &child); res != nil && res.t != requestrls.t {
				if src := checkSource(&requestrls, &child); src == nil || src.t != requestrls.t {
					parent = *res
					code = 201
					break
				}
			}

			if res := checkHDR(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 202
				break
			}

			if res := checkChannels(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 203
				break
			}

			if res := checkSource(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 204
				break
			}

			if res := checkAudio(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 205
				break
			}

			if res := checkExtension(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 206
				break
			}

			if res := checkLanguage(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 207
				break
			}

			if res := checkReplacement(&requestrls, &child); res != nil && res.t != requestrls.t {
				parent = *res
				code = 208
				break
			}
		}

		if code == -1 {
			http.Error(w, fmt.Sprintf("Cross submission: %q\n", req.Name), 250)
		} else if code != 0 {
			http.Error(w, fmt.Sprintf("Not an upgrade submission: %q => %q\n", req.Name, parent.t.Name), code)
		} else {
			http.Error(w, fmt.Sprintf("Upgrade submission: %q\n", req.Name), 200)
		}
	} else {
		http.Error(w, fmt.Sprintf("Unique submission: %q\n", req.Name), 200)
	}
}

func handleCross(w http.ResponseWriter, r *http.Request) {
	var req upgradereq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 470)
		return
	}

	if len(req.Name) == 0 {
		http.Error(w, fmt.Sprintf("No title passed.\n"), 469)
		return
	}

	if err := getClient(&req); err != nil {
		http.Error(w, fmt.Sprintf("Unable to get client: %q\n", err), 471)
		return
	}

	mp := req.getAllTorrents()
	if mp.err != nil {
		http.Error(w, fmt.Sprintf("Unable to get result: %q\n", mp.err), 468)
		return
	}

	requestrls := Entry{r: rls.ParseString(req.Name)}
	v, ok := mp.e[getFormattedTitle(requestrls.r)]
	if !ok {
		http.Error(w, fmt.Sprintf("Not a cross-submission: %q\n", req.Name), 420)
		return
	}

	if t, err := base64.StdEncoding.DecodeString(strings.Trim(strings.TrimSpace(string(req.Torrent)), `"`)); err == nil {
		req.Torrent = t
	} else {
		t := strings.Trim(strings.TrimSpace(string(req.Torrent)), `\"[`)
		b := make([]byte, 0, len(t)/3)

		for {
			r, valid, z := Atoi(t)
			if !valid {
				break
			}

			b = append(b, byte(r))
			t = z
		}

		if len(b) != 0 {
			req.Torrent = b
		}
	}

	for _, child := range v {
		if rls.Compare(requestrls.r, child.r) != 0 || child.t.Progress != 1.0 {
			continue
		}

		m, err := req.getFiles(child.t.Hash)
		if err != nil {
			fmt.Printf("Failed to get Files %q: %q\n", req.Name, err)
			continue
		}

		dirLayout := false
		for _, v := range *m {
			dirLayout = strings.HasPrefix(v.Name, child.t.Name)
			break
		}

		cat := child.t.Category
		if strings.Contains(cat, ".cross-seed") == false {
			cats, err := req.getCategories()
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to get categories (%q): %q\n", child.t.Name, mp.err), 466)
				return
			}

			if v := cats[cat]; ok {
				save := v.SavePath
				if len(save) == 0 {
					save = cat
				}

				cat += ".cross-seed"

				if err := req.createCategory(cat, save); err != nil {
					http.Error(w, fmt.Sprintf("Failed to create new category (%q): %q\n", cat, mp.err), 466)
					return
				}
			}
		}

		opts := &qbittorrent.TorrentAddOptions{
			SkipHashCheck: true,
			Category:      cat,
			Tags:          "upgraderr",
			Paused:        true,
		}

		if dirLayout {
			opts.ContentLayout = qbittorrent.ContentLayoutSubfolderCreate
		} else {
			opts.ContentLayout = qbittorrent.ContentLayoutSubfolderNone
		}

		if err := req.submitTorrent(opts); err != nil {
			http.Error(w, fmt.Sprintf("Failed cross submission upload (%q): %q\n", req.Name, err), 460)
			return
		}

		for i := 0; i < 56; i++ {
			t, err := req.getTorrent()
			if err != nil {
				fmt.Printf("Couldn't find %q: %q\n", req.Name, err)
				continue
			}

			if len(req.Hash) == 0 {
				req.Hash = t.Hash
				fmt.Printf("FOUND: %#v\n", t)
				i = 0
			}

			fmt.Printf("State: %#v\n", t)
			switch t.State {
			case qbittorrent.TorrentStateMissingFiles:
				req.recheckTorrent()
			case qbittorrent.TorrentStatePausedUp:
				if err := req.resumeTorrent(); err != nil {
					break
				}

				for k := 0; k < 12; k++ {
					req.announceTrackers()
					trackers, _ := req.getTrackers()
					good := false
					for _, tr := range trackers {
						if tr.Status == qbittorrent.TrackerStatusOK {
							good = true
							break
						}
					}

					if good {
						break
					}

					time.Sleep(4)
				}

			case qbittorrent.TorrentStatePausedDl:
				if t.Progress < 0.8 {
					if err := req.deleteTorrent(); err == nil {
						http.Error(w, fmt.Sprintf("Name matched, data did not on cross: %q\n", req.Name), 427)
						return
					}

					break
				}

				files, err := req.getFiles(req.Hash)
				if err != nil {
					break
				}

				damage := false
				for _, f := range *files {
					if f.Progress == 1.0 {
						continue
					}

					damage = true
					break
				}

				if damage == false {
					if err := req.resumeTorrent(); err != nil {
						http.Error(w, fmt.Sprintf("Unable to resume valid cross: %q\n", req.Name), 480)
						return
					}

					break
				}

				if err := req.deleteTorrent(); err != nil {
					http.Error(w, fmt.Sprintf("Unable to delete existing torrent: %q | %q | %q\n", req.Name, req.Hash, err), 424)
					return
				}

				/* This is still the old Torrent. */
				atm := t.AutoManaged
				oldpath := t.SavePath
				opts.SavePath = t.SavePath + "/.tmp"
				if err := req.submitTorrent(opts); err != nil {
					http.Error(w, fmt.Sprintf("Failed to adv cross: %q\n", req.Name), 455)
					req.deleteTorrent()
					return
				}

				for t.State = "check"; strings.Contains(string(t.State), "check"); t, err = req.getTorrent() {
					if err != nil {
						t.State = "check"
					}
				}

				for _, f := range *files {
					if f.Progress == 1.0 {
						continue
					}

					for _, pf := range *m {
						if pf.Name != f.Name {
							continue
						}

						np := ""
						if idx := strings.LastIndex(f.Name, "/"); idx != -1 {
							np = f.Name[:idx] + t.Hash + " " + f.Name[idx+1:]
						} else {
							np = t.Hash + " " + f.Name
						}

						req.renameFile(req.Hash, f.Name, np) /* if it fails. so be it. */
					}
				}

				if err := req.setLocationTorrent(oldpath); err != nil {
					http.Error(w, fmt.Sprintf("Failed to change save location: %q | %q\n", req.Name, err), 435)
					return
				}

				if t.AutoManaged != atm {
					if err := req.setTorrentManagement(atm); err != nil {
						http.Error(w, fmt.Sprintf("Failed to ATM: %q | %q\n", req.Name, err), 433)
						return
					}
				}

				if err := req.recheckTorrent(); err != nil {
					http.Error(w, fmt.Sprintf("Failed to Recheck: %q | %q\n", req.Name, err), 431)
					return
				}

				if err := req.resumeTorrent(); err != nil {
					http.Error(w, fmt.Sprintf("Failed to Resume: %q | %q\n", req.Name, err), 429)
					return
				}
			case qbittorrent.TorrentStateCheckingUp, qbittorrent.TorrentStateCheckingDl, qbittorrent.TorrentStateCheckingResumeData:
				i--
			}
		}

		http.Error(w, fmt.Sprintf("Unable to get paused torrents: %q\n", err), 450)
		return
	}

	http.Error(w, fmt.Sprintf("Failed to cross: %q\n", req.Name), 430)
}

func getFormattedTitle(r rls.Release) string {
	s := fmt.Sprintf("%s%s%s%04d%02d%02d%02d%03d", rls.MustNormalize(r.Artist), rls.MustNormalize(r.Title), rls.MustNormalize(r.Subtitle), r.Year, r.Month, r.Day, r.Series, r.Episode)
	for _, a := range r.Cut {
		s += rls.MustNormalize(a)
	}

	for _, a := range r.Edition {
		s += rls.MustNormalize(a)
	}

	return s
}

func checkExtension(requestrls, child *Entry) *Entry {
	sm := map[string]int{
		"mkv":  90,
		"mp4":  89,
		"webp": 88,
		"ts":   87,
		"wmv":  86,
		"xvid": 85,
		"divx": 84,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := sm[e.Ext]

		if i == 0 {
			if len(e.Ext) != 0 {
				fmt.Printf("UNKNOWNEXT: %q\n", e.Ext)
			}

			i = sm["divx"]
		}

		return i
	})
}

func checkLanguage(requestrls, child *Entry) *Entry {
	sm := map[string]int{
		"ENGLiSH": 2,
		"MULTi":   1,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := 0
		for _, v := range e.Language {
			if i < sm[v] {
				i = sm[v]
			}
		}

		if i == 0 {
			if len(e.Language) != 0 {
				fmt.Printf("UNKNOWNLANGUAGE: %q\n", e.Language)
			} else {
				i = sm["ENGLiSH"]
			}
		}

		return i
	})
}

func checkReplacement(requestrls, child *Entry) *Entry {
	if rls.MustNormalize(child.r.Group) != rls.MustNormalize(requestrls.r.Group) {
		return nil
	}

	sm := map[string]int{
		"COMPLETE":   0,
		"REMUX":      1,
		"EXTENDED":   2,
		"REMASTERED": 3,
		"PROPER":     4,
		"REPACK":     5,
		"INTERNAL":   6,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := 0
		for _, v := range e.Other {
			if i < sm[v] {
				i = sm[v]
			}
		}

		if i == 0 && len(e.Other) != 0 {
			fmt.Printf("UNKNOWNOTHER: %q\n", e.Other)
		}

		return i
	})
}

func checkAudio(requestrls, child *Entry) *Entry {
	sm := map[string]int{
		"DTS-HD.HRA": 90,
		"DDPA":       89,
		"TrueHD":     88,
		"DTS-HD.MA":  87,
		"DTS-HD.HR":  86,
		"Atmos":      85,
		"DTS-HD":     84,
		"DDP":        83,
		"DD":         82,
		"OPUS":       81,
		"AAC":        80,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := 0
		for _, v := range e.Audio {
			if i < sm[v] {
				i = sm[v]
			}
		}

		if i == 0 {
			if len(e.Audio) != 0 {
				fmt.Printf("UNKNOWNAUDIO: %q\n", e.Audio)
			}

			i = sm["AAC"]
		}

		return i
	})
}

func checkSource(requestrls, child *Entry) *Entry {
	if child.r.Source == requestrls.r.Source {
		return nil
	}

	sm := map[string]int{
		"WEB-DL":     90,
		"UHD.BluRay": 89,
		"BluRay":     88,
		"WEB":        87,
		"WEBRiP":     86,
		"BDRiP":      85,
		"HDRiP":      84,
		"HDTV":       83,
		"DVDRiP":     82,
		"HDTC":       81,
		"HDTS":       80,
		"TC":         79,
		"VHSRiP":     78,
		"WORKPRiNT":  77,
		"TS":         76,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := sm[e.Source]

		if i == 0 {
			if len(e.Source) != 0 {
				fmt.Printf("UNKNOWNSRC: %q\n", e.Source)
			}

			i = sm["TS"]
		}

		return i
	})
}

func checkChannels(requestrls, child *Entry) *Entry {
	if child.r.Channels == requestrls.r.Channels {
		return nil
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i, _ := strconv.ParseFloat(e.Channels, 8)

		if i == 0.0 {
			i = 2.0
		}

		return int(i * 10)
	})
}

func checkHDR(requestrls, child *Entry) *Entry {
	sm := map[string]int{
		"DV":     90,
		"HDR10+": 89,
		"HDR10":  88,
		"HDR+":   87,
		"HDR":    86,
		"HLG":    85,
		"SDR":    84,
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i := 0
		for _, v := range e.HDR {
			if i < sm[v] {
				i = sm[v]
			}
		}

		if i == 0 {
			if len(e.HDR) != 0 {
				fmt.Printf("UNKNOWNHDR: %q\n", e.HDR)
			}

			i = sm["SDR"]
		}

		return i
	})
}

func checkResolution(requestrls, child *Entry) *Entry {
	if child.r.Resolution == requestrls.r.Resolution {
		return nil
	}

	return compareResults(requestrls, child, func(e rls.Release) int {
		i, _, _ := Atoi(e.Resolution)
		if i == 0 {
			i = 480
		}

		return i
	})
}

func compareResults(requestrls, child *Entry, f func(rls.Release) int) *Entry {
	requestrlsv := f(requestrls.r)
	childv := f(child.r)

	if childv > requestrlsv {
		return child
	} else if requestrlsv > childv {
		return requestrls
	}

	return nil
}

func Normalize(buf string) string {
	return strings.ToLower(strings.TrimSpace(strings.ToValidUTF8(buf, "")))
}

func Atoi(buf string) (ret int, valid bool, pos string) {
	if len(buf) == 0 {
		return ret, false, buf
	}

	i := 0
	for ; unicode.IsSpace(rune(buf[i])); i++ {
	}

	r := buf[i]
	if r == '-' || r == '+' {
		i++
	}

	for ; i != len(buf); i++ {
		d := int(buf[i] - '0')
		if d < 0 || d > 9 {
			break
		}

		valid = true
		ret *= 10
		ret += d
	}

	if r == '-' {
		ret *= -1
	}

	return ret, valid, buf[i:]
}

func BoolPointer(b bool) *bool {
	// CC ze0s
	return &b
}

func StringPointer(s string) *string {
	return &s
}
