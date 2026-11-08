// Package source finds Pinball 2000 update ROM sets in directories, plain
// ROM files, ZIP archives, PKZIP self-extracting EXEs, nested archives and
// InstallShield 3 installers (Update Manager / PUB card manager packages).
package source

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whaslbeck/groundcontrolp2k/internal/is3"
	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// maxFileSize limits what is read into memory (updates are a few MB).
const maxFileSize = 128 << 20

// maxDepth limits archive nesting (zip in zip in exe ...).
const maxDepth = 4

// entry is one file found while scanning, possibly inside archives.
type entry struct {
	container string // top-level file or directory given by the user
	origin    string // full display path, archives joined with "!"
	dir       string // grouping key: origin of the directory holding it
	name      string // base name
	data      []byte
}

// Result of a scan.
type Result struct {
	Sets     []*romset.Set
	Warnings []string
}

type scanner struct {
	entries  []entry
	warnings []string
}

// Scan searches the given paths (files or directories, recursively).
func Scan(paths ...string) *Result {
	sc := &scanner{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			sc.warn("%v", err)
			continue
		}
		if st.IsDir() {
			filepath.WalkDir(p, func(fp string, d fs.DirEntry, err error) error {
				if err != nil {
					sc.warn("%v", err)
					return nil
				}
				if d.Type().IsRegular() {
					sc.file(fp)
				}
				return nil
			})
		} else {
			sc.file(p)
		}
	}
	return &Result{Sets: sc.group(), Warnings: sc.warnings}
}

func (sc *scanner) warn(f string, a ...any) { sc.warnings = append(sc.warnings, fmt.Sprintf(f, a...)) }

func (sc *scanner) file(fp string) {
	st, err := os.Stat(fp)
	if err != nil || st.Size() > maxFileSize {
		return
	}
	if !interesting(filepath.Base(fp)) {
		return
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		sc.warn("%v", err)
		return
	}
	// the "source" shown to the user is the physical file for archives and
	// the directory for loose ROM files
	src := fp
	if _, _, ok := romset.ParseRomName(filepath.Base(fp)); ok || strings.EqualFold(filepath.Base(fp), "gamelist.txt") {
		src = filepath.Dir(fp)
	}
	sc.add(src, fp, filepath.Dir(fp), filepath.Base(fp), data, 0)
}

// interesting filters files by name before reading them.
func interesting(name string) bool {
	l := strings.ToLower(name)
	if _, _, ok := romset.ParseRomName(name); ok {
		return true
	}
	return l == "gamelist.txt" || strings.HasSuffix(l, ".zip") || strings.HasSuffix(l, ".exe")
}

// add classifies one file by content and recurses into archives.
func (sc *scanner) add(container, origin, dir, name string, data []byte, depth int) {
	if _, _, ok := romset.ParseRomName(name); ok || strings.EqualFold(name, "gamelist.txt") {
		sc.entries = append(sc.entries, entry{container, origin, dir, name, data})
		return
	}
	if depth >= maxDepth {
		return
	}
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		sc.zip(container, origin, zr, depth)
		return
	}
	if bytes.HasPrefix(data, []byte("MZ")) {
		if sets, err := is3.Find(data); err == nil {
			sc.is3(container, origin, sets, depth)
		}
	}
}

func (sc *scanner) zip(container, origin string, zr *zip.Reader, depth int) {
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > maxFileSize {
			continue
		}
		name := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		if !interesting(name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			sc.warn("%s!%s: %v", origin, f.Name, err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			sc.warn("%s!%s: %v", origin, f.Name, err)
			continue
		}
		inner := origin + "!" + f.Name
		sc.add(container, inner, origin+"!"+path.Dir(f.Name), name, data, depth+1)
	}
}

func (sc *scanner) is3(container, origin string, sets []*is3.Set, depth int) {
	for _, s := range sets {
		for _, f := range s.Files {
			name := path.Base(f.Name)
			if !interesting(name) {
				continue
			}
			data, err := f.Data()
			if err != nil {
				sc.warn("%s!%s: %v", origin, f.Name, err)
				continue
			}
			sc.add(container, origin+"!"+f.Name, origin+"!"+path.Dir(f.Name), name, data, depth+1)
		}
	}
}

// group turns the flat list of entries into ROM sets.
func (sc *scanner) group() []*romset.Set {
	byDir := map[string][]entry{}
	var dirs []string
	gameLists := map[string]map[uint32]string{} // by container
	for _, e := range sc.entries {
		if strings.EqualFold(e.name, "gamelist.txt") {
			gl := romset.ParseGameList(e.data)
			if gameLists[e.container] == nil {
				gameLists[e.container] = map[uint32]string{}
			}
			for k, v := range gl {
				gameLists[e.container][k] = v
			}
			continue
		}
		if _, ok := byDir[e.dir]; !ok {
			dirs = append(dirs, e.dir)
		}
		byDir[e.dir] = append(byDir[e.dir], e)
	}

	var sets []*romset.Set
	for _, d := range dirs {
		es := byDir[d]
		for _, e := range es {
			prefix, k, _ := romset.ParseRomName(e.name)
			if k != romset.PartBootData {
				continue
			}
			s := &romset.Set{Source: e.container, Prefix: prefix, GameNames: gameLists[e.container]}
			s.FileGameID, s.FileVersion = romset.PrefixInfo(prefix)
			if bd, err := romset.ParseBootData(e.data); err == nil {
				s.Boot = bd
			}
			// same prefix first
			for _, o := range es {
				p2, k2, _ := romset.ParseRomName(o.name)
				if strings.EqualFold(p2, prefix) && s.Parts[k2] == nil {
					s.Parts[k2] = &romset.Part{Kind: k2, Name: o.name, Origin: o.origin, Data: o.data}
				}
			}
			// then a unique file of a missing kind from the same directory
			// (rfm_200.zip ships pin2000_50070_0191_sf.rom with 2.00)
			for k2 := romset.Kind(0); k2 <= romset.PartPubBoot; k2++ {
				if s.Parts[k2] != nil {
					continue
				}
				var cand []entry
				for _, o := range es {
					if _, ko, _ := romset.ParseRomName(o.name); ko == k2 {
						cand = append(cand, o)
					}
				}
				if len(cand) == 1 {
					s.Parts[k2] = &romset.Part{Kind: k2, Name: cand[0].name, Origin: cand[0].origin,
						Data: cand[0].data, Note: "file name belongs to another version"}
				}
			}
			sets = append(sets, s)
		}
	}
	borrowMissing(sets)
	sets = dedupe(sets)
	sort.SliceStable(sets, func(i, j int) bool {
		a, b := sets[i], sets[j]
		if a.GameID() != b.GameID() {
			return a.GameID() < b.GameID()
		}
		if a.Version() != b.Version() {
			return versionLess(a.Version(), b.Version())
		}
		return a.Source < b.Source
	})
	return sets
}

// borrowMissing fills a missing im_flsh0/game/symbols from another set
// when size and checksum recorded in bootdata match exactly (the 1.80
// release, for example, ships without im_flsh0.rom).
func borrowMissing(sets []*romset.Set) {
	for _, s := range sets {
		for _, k := range []romset.Kind{romset.PartImFlsh0, romset.PartGame, romset.PartSymbols} {
			if c := s.CheckPart(k); c.Status == romset.StatusOK {
				continue
			}
			size, sum, ok := s.Expected(k)
			if !ok {
				continue
			}
			for _, o := range sets {
				p := o.Parts[k]
				if o == s || p == nil || p.Note != "" || len(p.Data) < size {
					continue
				}
				if romset.Sum32(p.Data, uint32(size/4)) == sum {
					s.Parts[k] = &romset.Part{Kind: k, Name: p.Name, Origin: p.Origin,
						Data: p.Data[:size], Note: "taken from " + o.Label() + " (identical size and checksum)"}
					break
				}
			}
		}
	}
}

// dedupe removes sets that are byte-identical to an earlier one.
func dedupe(sets []*romset.Set) []*romset.Set {
	seen := map[string]bool{}
	var out []*romset.Set
	for _, s := range sets {
		h := sha1.New()
		for _, p := range s.Parts {
			if p != nil {
				h.Write([]byte(p.Kind.String()))
				h.Write(p.Data)
			}
		}
		key := string(h.Sum(nil))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

func versionLess(a, b string) bool {
	var am, an, bm, bn int
	fmt.Sscanf(a, "%d.%d", &am, &an)
	fmt.Sscanf(b, "%d.%d", &bm, &bn)
	if am != bm {
		return am < bm
	}
	return an < bn
}
