// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Laporan Pengujian Penetrasi: %s\n\n", nz(in.Title, "Tugas Tanpa Nama"))
	fmt.Fprintf(&b, "- **Tujuan Tugas**: %s\n", nz(in.Goal, "(tidak ditentukan)"))
	fmt.Fprintf(&b, "- **Waktu Dibuat**: %s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## Ringkasan\n\n")
	fmt.Fprintf(&b, "- Temuan terkonfirmasi: **%d** item\n", len(in.Findings))
	fmt.Fprintf(&b, "- Aset: ")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## Temuan\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_Tidak ada kerentanan terkonfirmasi kali ini._\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n))
		}
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "Tidak Berkategori")))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / Bukti:**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
