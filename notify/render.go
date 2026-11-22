package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes memotong s agar tidak melebihi max byte, menjamin hasilnya
// tetap UTF-8 valid dan tidak memutus karakter di tengah.
//
// Mengapa harus dipotong di batas karakter: markdown bot grup WeCom Work
// punya batas keras 4096 **byte** (bukan jumlah karakter), sementara satu
// karakter Tionghoa memakan 3 byte. Memotong langsung berdasarkan byte bisa
// membelah satu karakter jadi dua, menghasilkan UTF-8 yang tidak valid,
// akibatnya platform bisa menolak seluruh pesan atau menampilkannya sebagai
// kotak karakter rusak. Caranya di sini adalah mundur dari posisi batas ke
// byte awal rune terdekat (utf8.RuneStart menentukan byte lanjutan
// 0b10xxxxxx).
//
// max<=0 berarti tidak dibatasi. Setelah dipotong akan ditambahkan elipsis,
// kecuali max terlalu kecil untuk menampung elipsis.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max lebih pendek dari elipsis: lupakan elipsis, potong murni saja,
		// agar hasilnya tidak justru melebihi max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine memadatkan teks multi-baris jadi satu baris: melipat semua
// whitespace, lalu memotong berdasarkan jumlah karakter. Dipakai untuk baris
// judul pesan IM, ringkasan sering mengandung baris baru, kalau langsung
// dimasukkan ke tabel/judul akan merusak tata letak.
// max<=0 berarti panjang tidak dibatasi.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes memotong s agar tidak melebihi max karakter (bukan byte),
// menambahkan elipsis jika melebihi. max<=0 berarti tidak dibatasi.
//
// Perbedaannya dengan TruncateBytes ada pada satuan platform: WeCom Work
// membatasi panjang berdasarkan byte, Telegram berdasarkan jumlah karakter.
// Memakai satuan yang salah tidak akan menimbulkan error, hanya membuat
// pesan terpotong jauh lebih pendek dari seharusnya (1 karakter Tionghoa =
// 3 byte, dipotong berdasarkan byte 4096 hanya menyisakan sekitar 1365
// karakter), karena itu kedua fungsi ini harus tetap dipertahankan dan
// dipilih sesuai kanalnya.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML memotong fragmen HTML berdasarkan jumlah karakter, dan
// menjamin tidak menghasilkan tag yang terpotong separuh.
//
// Memotong HTML langsung berdasarkan karakter bisa menghasilkan tag cacat
// seperti `<a href="htt`, yang membuat parser platform menolak seluruh
// pesan atau justru menelan isi berikutnya sebagai nilai atribut. Caranya
// di sini: potong dulu berdasarkan karakter, lalu periksa apakah ujungnya
// ada `<` yang belum ditutup, kalau ada mundurkan ke sebelum tanda itu.
//
// Tidak melakukan penyeimbangan tag (melengkapi </b> dan sejenisnya):
// parser HTML Telegram otomatis menutup tag yang belum ditutup, sementara
// mengimplementasikan penyeimbangan sendiri harus menangani tanda kutip di
// atribut, komentar, dan tag self-closing, kompleksitasnya tidak sepadan
// dengan manfaatnya.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// Jika ujung potongan dimulai dengan `<` (setelah `<` terakhir tidak ada `>`), mundurkan ke sebelum `<` itu.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// Jika ujungnya adalah entitas HTML yang terpotong (misalnya `&amp;` terpotong jadi `&amp`), mundurkan juga.
	// Potongan entitas pada parser yang hanya mengenali entitas lengkap bisa membuat **seluruh pesan** ditolak,
	// padahal pesan ringkasan yang melebihi batas panjang memang sering terjadi, tidak sepadan jika sampai kehilangan seluruh notifikasi karena ini.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount menghitung berapa item yang bisa dimuat **secara utuh**
// dalam anggaran yang ada, dipakai pesan ringkasan untuk mengemas per item
// utuh.
//
// Mengapa harus per item utuh, bukan merender semuanya dulu lalu dipotong:
// pemotongan akan membuat item di bagian belakang hilang begitu saja, padahal
// catatan pengirimannya tetap ditandai terkirim, tidak terlihat di pesan
// maupun di riwayat pengiriman, temuan itu lenyap begitu saja. Dengan
// pengemasan per item utuh, item yang tidak termuat tetap tersimpan di
// database untuk jadi batch berikutnya, dan kept yang diterima pemanggil
// adalah jumlah item yang benar-benar terkirim pada pesan ini.
//
// Parameter: maxSize<=0 berarti tidak dibatasi; reserve adalah jatah yang
// disisakan untuk header/footer pesan; size bertugas mengukur panjang
// (satuannya berbeda tiap platform: WeCom Work/DingTalk berdasarkan byte,
// Telegram berdasarkan jumlah karakter, memakai satuan yang salah tidak
// menimbulkan error, hanya membuat pesan berbahasa non-ASCII tertekan jauh
// di bawah batas); render merender item ke-idx jadi teks aslinya, panjangnya
// berbeda-beda sesuai konten dan tidak bisa diperkirakan.
//
// Minimal mengembalikan 1 (selama masih ada item). Saat satu item saja
// sudah sangat panjang, item itu tetap harus dikirim dan ditangani oleh
// pemotongan akhir di sisi pemanggil, kalau tidak satu temuan yang sangat
// panjang akan membuat seluruh batch tertahan permanen.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize adalah dua satuan pengukuran untuk packItemCount,
// diberi nama agar di tempat pemanggilan tidak muncul closure
// func(s string) int polos, yang akan sulit dibaca satuan mana yang dipakai.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine merender daftar aset jadi satu baris teks tampilan, jika
// jumlahnya melebihi limit maka sisanya diringkas dan jumlah totalnya
// dicantumkan. Satu temuan bisa terkait puluhan aset, jika semua ditulis
// akan membuat pesan membengkak.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " dan lainnya (total " + itoa(len(assets)) + ")"
}

// itoa adalah alias singkat untuk strconv.Itoa, hanya dipakai untuk
// menyusun teks tampilan agar tidak perlu import strconv di banyak tempat.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
