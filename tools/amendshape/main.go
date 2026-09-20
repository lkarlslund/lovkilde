// Throwaway census for M027T002: where do numbered amendments actually live in the
// publisher's XML, and how many does Document.Amendments() currently miss?
//
// Feed it a list of file paths on stdin, one per line (the ones holding
// <AendringsNummer>). It prints one receipt line per enclosing shape.
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

type shape struct {
	path   string // ancestor names above the AendringsNummer, outermost first
	framed bool   // an ancestor is AendringCentreretParagraf: the walker sees this one
}

type tally struct {
	changes int
	docs    int
	example string
}

func main() {
	paths := os.Args[1:]
	if len(paths) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" {
				paths = append(paths, p)
			}
		}
	}

	var (
		parsed, withChanges int
		seen, claimed, lost int
		byShape             = map[string]*tally{}
		byRaw               = map[string]*tally{}
		loadErr             int
	)

	for _, p := range paths {
		doc, err := lexdania.Load(p)
		if err != nil {
			loadErr++
			continue
		}
		parsed++
		all := doc.Root.FindNamed("AendringsNummer")
		if len(all) == 0 {
			continue
		}
		withChanges++
		seen += len(all)

		got := len(doc.Amendments())
		claimed += got
		if got < len(all) {
			lost += len(all) - got
		}

		// Classify every AendringsNummer by the element chain above it.
		var stack []string
		var walk func(n *lexdania.Node)
		walk = func(n *lexdania.Node) {
			stack = append(stack, n.Name)
			if n.Name == "AendringsNummer" {
				s := classify(stack)
				key := s.path
				if !s.framed {
					key = "LOST " + key
				}
				t := byShape[key]
				if t == nil {
					t = &tally{}
					byShape[key] = t
				}
				t.changes++
				if !s.framed && got < len(all) {
					// Full chain for the lost ones: the collapsed shape says which
					// element a fix must understand, the raw chain says what it is.
					raw := strings.Join(stack[1:len(stack)-1], ">")
					r := byRaw[raw]
					if r == nil {
						r = &tally{}
						byRaw[raw] = r
					}
					r.changes++
					if r.example == "" {
						r.example = p
					}
				}
			}
			for _, c := range n.Elems {
				walk(c)
			}
			stack = stack[:len(stack)-1]
		}
		walk(doc.Root)
	}

	// docs per shape is a second pass over the same tally; count files here instead.
	fmt.Printf("files %d, parsed %d (load errors %d), files with AendringsNummer %d\n",
		len(paths), parsed, loadErr, withChanges)
	fmt.Printf("AendringsNummer seen %d, claimed by Amendments() %d, lost %d (%.1f%%)\n\n",
		seen, claimed, lost, 100*float64(lost)/float64(max1(seen)))

	keys := make([]string, 0, len(byShape))
	for k := range byShape {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return byShape[keys[i]].changes > byShape[keys[j]].changes })
	for _, k := range keys {
		t := byShape[k]
		fmt.Printf("%-70s %8d changes\n", k, t.changes)
	}

	rawKeys := make([]string, 0, len(byRaw))
	for k := range byRaw {
		rawKeys = append(rawKeys, k)
	}
	sort.Slice(rawKeys, func(i, j int) bool { return byRaw[rawKeys[i]].changes > byRaw[rawKeys[j]].changes })
	fmt.Println("\nRAW ANCESTOR CHAIN OF CHANGES THE WALKER DOES NOT CLAIM")
	for i, k := range rawKeys {
		if i >= 15 {
			break
		}
		fmt.Printf("%8d  %s\n         %s\n", byRaw[k].changes, k, byRaw[k].example)
	}
}

func max1(n int) int {
	if n == 0 {
		return 1
	}
	return n
}

// classify reports the chain above an AendringsNummer, collapsing the structural
// elements that say nothing about the shape (Overskrift, CenterSamlingsObjekt, …) and
// keeping the ones a fix would have to understand.
func classify(stack []string) shape {
	var keep []string
	s := shape{}
	for _, name := range stack[1 : len(stack)-1] { // drop root and the element itself
		switch name {
		case "AendringCentreretParagraf":
			s.framed = true
			keep = append(keep, "ACP")
		case "Paragraf", "Stk", "Afsnit", "Afsnit2", "Kapitel", "Section", "Overskrift", "Artikel", "Punkt", "Nr", "Tabel", "Indgang":
			keep = append(keep, name)
		}
	}
	s.path = strings.Join(keep, ">")
	if s.path == "" {
		s.path = "(root)"
	}
	return s
}
