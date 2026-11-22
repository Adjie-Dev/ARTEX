package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Rendering untuk fitur "ekspor" di halaman temuan: merender sekumpulan baris tabel findings menjadi Markdown ringkasan, Markdown per item,
// atau CSV. JSON diserialisasi langsung oleh layer server lewat DTO, tidak ditangani di sini.

// sortFindingsForExport mengurutkan berdasarkan tingkat keparahan menurun, lalu waktu menurun, sesuai dengan pengelompokan di laporan ringkasan.
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri != rj {
			return ri < rj // semakin kecil sevRank, semakin parah
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle mengambil judul temuan yang mudah dibaca: nama -> kategori -> "Tidak terklasifikasi".
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "Tidak terklasifikasi"))
}

// FindingsMarkdown menggabungkan sekumpulan findings menjadi satu laporan ringkasan (ringkasan + dikelompokkan berdasarkan tingkat keparahan,
// setiap item berisi kategori/status/tugas terkait/bukti/laporan detail).
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var b strings.Builder
	b.WriteString("# Laporan Ringkasan Temuan\n\n")
	fmt.Fprintf(&b, "- **Waktu dibuat**: %s\n", generatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Total temuan**: %d\n\n", len(items))

	// Ringkasan: hitungan per tingkat keparahan.
	counts := map[string]int{}
	for _, f := range items {
		counts[f.Severity]++
	}
	b.WriteString("## Ringkasan\n\n")
	b.WriteString("| Tingkat Keparahan | Jumlah |\n| --- | --- |\n")
	for _, s := range []struct{ key, label string }{
		{"critical", "Kritis"}, {"high", "Tinggi"}, {"medium", "Sedang"}, {"low", "Rendah"},
	} {
		fmt.Fprintf(&b, "| %s | %d |\n", s.label, counts[s.key])
	}
	b.WriteString("\n")

	if len(items) == 0 {
		b.WriteString("_Tidak ada temuan yang cocok._\n")
		return b.String()
	}

	b.WriteString("## Detail Temuan\n\n")
	for i, f := range items {
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
		if f.VulnClass != "" {
			fmt.Fprintf(&b, "- **Kategori**: %s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- **Status**: %s\n", nz(f.Status, "pending"))
		if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
			fmt.Fprintf(&b, "- **Tugas terkait**: %s\n", desc)
		}
		fmt.Fprintf(&b, "- **Waktu ditemukan**: %s\n\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
		if s := strings.TrimSpace(f.Summary); s != "" {
			fmt.Fprintf(&b, "%s\n\n", s)
		}
		if e := strings.TrimSpace(f.Evidence); e != "" {
			fmt.Fprintf(&b, "**Bukti:**\n\n```\n%s\n```\n\n", e)
		}
		if rep := strings.TrimSpace(f.Report); rep != "" {
			b.WriteString("**Laporan detail:**\n\n")
			b.WriteString(rep)
			b.WriteString("\n\n")
		}
		b.WriteString(findingTrafficMarkdown(f, false))
		b.WriteString("---\n\n")
	}
	return b.String()
}

// SingleFindingMarkdown merender satu temuan menjadi satu file Markdown terpisah (dipakai untuk paket "satu temuan satu file").
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
	if f.VulnClass != "" {
		fmt.Fprintf(&b, "- **Kategori**: %s\n", f.VulnClass)
	}
	fmt.Fprintf(&b, "- **Tingkat keparahan**: %s\n", nz(f.Severity, "info"))
	fmt.Fprintf(&b, "- **Status**: %s\n", nz(f.Status, "pending"))
	if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
		fmt.Fprintf(&b, "- **Tugas terkait**: %s\n", desc)
	}
	fmt.Fprintf(&b, "- **Waktu ditemukan**: %s\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Waktu dibuat**: %s\n\n", generatedAt.Format("2006-01-02 15:04:05"))
	if s := strings.TrimSpace(f.Summary); s != "" {
		fmt.Fprintf(&b, "## Ringkasan\n\n%s\n\n", s)
	}
	if e := strings.TrimSpace(f.Evidence); e != "" {
		fmt.Fprintf(&b, "## Bukti\n\n```\n%s\n```\n\n", e)
	}
	if rep := strings.TrimSpace(f.Report); rep != "" {
		b.WriteString("## Laporan Detail\n\n")
		b.WriteString(rep)
		b.WriteString("\n")
	}
	b.WriteString(findingTrafficMarkdown(f, true))
	return b.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}._-]+`)

// FindingFilename menghasilkan nama file .md yang aman untuk "satu temuan satu file", contohnya
// `critical_SQLInjection_#123.md`. Menghapus pemisah path dan karakter kontrol, mencegah path ilegal di dalam zip.
func FindingFilename(f *db.DBFinding) string {
	sev := nz(f.Severity, "info")
	title := findingTitle(f)
	name := fmt.Sprintf("%s_%s_#%d", sev, title, f.ID)
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = fmt.Sprintf("finding_%d", f.ID)
	}
	// Defensif: path dilucuti sekali lagi untuk mencegah zip slip.
	name = path.Base(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".md"
}

// FindingsCSV merender sekumpulan findings menjadi CSV (dengan UTF-8 BOM, agar Excel dapat mengenali karakter non-ASCII dengan benar).
// Tidak menyertakan teks lengkap report/evidence yang panjang, hanya field ringkasan; untuk teks lengkap gunakan ekspor Markdown/JSON.
func FindingsCSV(fs []*db.DBFinding) []byte {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Nama", "Kategori", "Tingkat Keparahan", "Status", "Tugas Terkait", "Waktu Ditemukan", "Ringkasan", "Jumlah Bukti Traffic", "ID Bukti Traffic"})
	for _, f := range items {
		_ = w.Write([]string{
			fmt.Sprintf("%d", f.ID),
			findingTitle(f),
			f.VulnClass,
			nz(f.Severity, "info"),
			nz(f.Status, "pending"),
			f.TaskDescription,
			f.CreatedAt.Format("2006-01-02 15:04:05"),
			strings.TrimSpace(f.Summary),
			fmt.Sprint(len(f.TrafficBindings)), findingTrafficIDs(f),
		})
	}
	w.Flush()
	return buf.Bytes()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids := make([]string, 0, len(f.TrafficBindings))
	for _, b := range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale := f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 && !stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Bukti Traffic Terkait\n\n")
	fmt.Fprintf(&out, "Versi bukti: %d; jumlah terikat: %d.\n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("Bukti telah berubah, laporan detail menunggu pembaruan.\n\n")
	}
	for i, b := range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **Bukti #%d - %s**: `%s %s`, kode status %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note != "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, "   [Request](evidence/%d/%d/request.http) · [Response](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
