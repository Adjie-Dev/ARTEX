package notify

import (
	"fmt"
	"strings"
)

// File ini berisi rendering pesan yang dipakai bersama oleh channel bertipe
// "Markdown" (DingTalk, WeCom).
// Feishu memakai kartu JSON, Telegram memakai HTML, email memakai HTML,
// masing-masing dirender di adapternya sendiri.

// maxAssetsShown adalah jumlah maksimum asset yang ditampilkan dalam pesan.
// Satu kerentanan bisa terhubung ke puluhan asset, menampilkan semuanya akan
// membuat pesan penuh sesak tanpa nilai informasi tambahan, domain setelah
// yang keempat tidak akan dibaca orang di chat IM.
const maxAssetsShown = 3

// maxSummaryRunes adalah jumlah karakter maksimum setelah ringkasan dipadatkan.
// Pesan IM hanya "mengarahkan untuk melihat detail", bukan isi laporan itu
// sendiri, isi lengkapnya ada di platform.
const maxSummaryRunes = 120

// markdownReservedBytes dicadangkan untuk kepala pesan (baris ringkasan +
// distribusi level + kemungkinan catatan pemotongan) dan ekor pesan (tautan
// platform). Saat mengepak pesan utuh, bagian ini dikurangi dari anggaran
// byte agar kepala dan ekor tidak ikut terpotong, karena begitu bagian itu
// terpotong, pembaca bahkan tidak bisa tahu "ini batch yang mana, dan berapa
// item lagi yang belum ditampilkan".
const markdownReservedBytes = 320

// markdownEscape melakukan escaping untuk karakter spesial markdown.
//
// Mengapa ini wajib dilakukan: judul kerentanan, ringkasan, jenis, dan nama
// tampilan asset semuanya berasal dari **sumber yang tidak terpercaya**,
// judul dan ringkasan berasal dari keluaran model (model membaca respons dari
// target yang diuji), sedangkan url asset adalah URL lengkap hasil scanning
// (termasuk query string yang bisa dikendalikan oleh target). Tanpa
// escaping, sebuah kerentanan dengan judul seperti
//
//	Login SQL Injection\n[Darurat: klik di sini untuk verifikasi akun](http://attacker.tld)
//
// akan dirender menjadi **tautan eksternal yang bisa diklik** di DingTalk/
// Feishu milik security engineer; sedangkan `![](http://attacker.tld/beacon)`
// akan diambil otomatis oleh klien saat dirender, yang sama saja dengan
// memberi tahu penyerang bahwa "kerentanan ini sudah dilihat" dan membocorkan
// IP pembaca. Bahkan konten yang tidak berbahaya pun, teks tebal atau
// blockquote yang disuntikkan bisa mendorong kerentanan parah di bawahnya
// keluar dari batas yang terlihat.
//
// Kumpulan karakter yang di-escape mencakup judul/tautan/penekanan/list/quote/
// strikethrough, yaitu karakter yang bisa mengubah struktur atau menghasilkan
// elemen yang bisa diklik. `\` harus diproses paling awal, jika tidak, garis
// miring terbalik yang ditambahkan belakangan akan ikut di-escape lagi.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText memadatkan teks yang tidak terpercaya menjadi satu baris lalu
// melakukan escaping, dipakai untuk isi markdown.
// Pemadatan jadi satu baris adalah separuh lain dari escaping: baris baru
// saja sudah bisa memalsukan item list atau blockquote baru, dan karakter
// escape tidak bisa mencegah itu.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle mengembalikan judul pesan (judul bar/judul kartu di platform
// IM), isinya adalah **teks asli tanpa escaping**.
//
// Escaping sengaja tidak dilakukan di sini: judul ini dipakai bersama oleh
// empat renderer dengan konteks berbeda, isi markdown, HTML milik Telegram,
// plain_text kartu Feishu, serta JSON Webhook umum dan subjek email. Aturan
// escaping di setiap konteks berbeda-beda (escaping markdown yang dimasukkan
// ke HTML akan menyisakan garis miring terbalik yang terlihat, dimasukkan ke
// JSON akan mencemari data), sehingga escaping harus dilakukan oleh masing-
// masing sisi output, lihat writeItem / feishuItemLines / telegramEscape.
// Pernah ditambahkan escaping markdown di fungsi bersama ini, akibatnya pesan
// Telegram menampilkan garis miring terbalik yang terlihat seperti `\(1\)`.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Ringkasan Kerentanan . Total %d item", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Notifikasi Kerentanan"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody merender isi pesan, mengembalikan isi pesan beserta **jumlah
// item yang benar-benar ditulis**.
//
// Nilai kembali kept adalah jumlah item yang benar-benar terkirim pada
// pengiriman ini, pemanggil berdasarkan ini hanya menandai kept item pertama
// sebagai sudah terkirim, item yang terpotong karena batas panjang channel
// harus disimpan untuk batch berikutnya, bukan ikut ditandai berhasil. Inilah
// sumber "kehilangan diam-diam": pesan terpotong, tapi catatan pengiriman
// menunjukkan semua sudah terkirim, tidak ada tempat yang menunjukkan bahwa
// separuh bagian belakang tidak pernah dikirim.
//
// maxBytes<=0 berarti tidak ada batas.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// Pesan tunggal tetap dikirim meskipun terlalu panjang (dipotong
		// sebagai jalan terakhir): sebagian informasi kerentanan lebih baik
		// daripada tidak terkirim sama sekali.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[Lihat semua di platform](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro merender bagian awal pesan ringkasan: rentang waktu,
// jumlah item, dan distribusi level.
// Dengan ini, penerima ringkasan bisa menilai apakah batch ini perlu segera
// ditangani tanpa harus membuka platform.
//
// items adalah item yang **benar-benar tertampung**, total adalah jumlah
// total yang seharusnya ada pada batch ini. Jika kedua angka berbeda, harus
// dijelaskan dengan jelas "masih ada berapa item di pesan berikutnya", kalau
// tidak pembaca akan mengira angka di kepala pesan itu adalah jumlah semua
// item, padahal item yang tidak pernah terkirim itu sama sekali tidak
// terlihat di antarmuka.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%d kerentanan baru dalam %d menit terakhir**", total, m.WindowMinutes)
	} else {
		fmt.Fprintf(&b, "**%d kerentanan baru**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (pesan ini menampilkan %d item pertama, %d item lainnya akan dilanjutkan di pesan berikutnya)", len(items), extra)
	}
	// Tampilkan distribusi per level, agar pembaca langsung tahu apakah ada
	// item kritis. Hanya menghitung item yang **benar-benar termuat** di
	// pesan ini, memastikan angka "kritis 3" konsisten dengan item yang bisa
	// dihitung di bawahnya.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem merender satu item kerentanan.
//
// prefix dipakai untuk nomor urut di daftar ringkasan; single=true merender
// versi lengkap (termasuk ringkasan dan tautan balik), di daftar ringkasan
// hanya dirender satu baris ringkasan, kalau tidak, ringkasan 50 item akan
// menjadi dokumen yang sangat panjang.
//
// Semua konten dari luar (judul/jenis/asset/ringkasan) melewati markdownText:
// dipadatkan jadi satu baris lalu di-escape. Tautan balik dibentuk dari
// public_base_url yang dikonfigurasi admin, bukan konten yang tidak
// terpercaya, dan harus berupa tautan yang bisa diklik, sehingga ditulis apa
// adanya.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Mode ringkasan: ditampilkan satu baris, asset dan ringkasan
		// dipadatkan lalu ditempel di belakang.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Perubahan Status**: %s -> %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Jenis**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Asset**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Ringkasan**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[Lihat detail](%s)\n", it.DetailURL)
	}
}
