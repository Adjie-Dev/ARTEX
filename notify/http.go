package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets menentukan apakah pesan boleh dikirim ke alamat loopback / link-local.
//
// Default menolak. Rentang alamat ini bukan tempat bot IM atau server email publik biasanya berada, dan yang
// bisa dijangkau lewat sana cukup sensitif: port admin layanan lain di mesin yang sama, serta endpoint metadata
// cloud (169.254.169.254, yang bisa membocorkan kredensial instance). Alamat pengiriman memang dikonfigurasi admin, tapi
// sesi admin yang dibajak lewat XSS/CSRF, atau orang lain yang berbagi JWT yang sama, bisa membaca kembali isi respons
// hanya dengan mengubah konfigurasi, karena doJSON menulis 200 byte pertama dari body respons 4xx/5xx ke last_error,
// dan endpoint riwayat pengiriman menampilkannya kembali. Ini jadi primitif pembacaan semi-blind.
//
// Tapi "SMTP relay lokal" (postfix di 127.0.0.1:25) adalah konfigurasi umum untuk mail server sendiri, dan
// pelarangan total akan membuat orang terjebak. Karena itu disediakan jalan keluar yang eksplisit, bukan izin yang
// di-hardcode: set ARTEX_NOTIFY_ALLOW_LOCAL=1 untuk mengizinkannya.
//
// Diekspor sebagai AllowLocalTargetsEnv agar pengujian bisa mengaktifkannya secara eksplisit, karena test case di
// paket ini dan paket server banyak memakai penerima tiruan httptest di 127.0.0.1, yang tanpa ini akan diblokir semua oleh guard.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP melaporkan apakah IP tujuan termasuk rentang alamat yang "secara default tidak diizinkan untuk pengiriman".
//
// Hanya menolak loopback, link-local (termasuk metadata cloud 169.254.169.254), unspecified, dan multicast.
// **Tidak menolak** jaringan privat RFC1918: Mattermost / SMTP relay internal yang dibangun sendiri adalah penggunaan
// sah yang umum, dan memblokirnya sekalian akan membuat fitur ini langsung tidak bisa dipakai di lingkungan nyata.
// Keputusan ini sengaja diambil: perlindungan harus menghalangi target yang benar-benar sensitif, tanpa merusak
// deployment yang normal.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) harus dikembalikan ke bentuk IPv4 sebelum diperiksa, jika tidak pemeriksaan ini bisa dilewati.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial adalah hook Control pada dialer http.Transport, yang memeriksa alamat tujuan
// **pada saat koneksi dibuat**.
//
// Mengapa diletakkan di tahap dial, bukan hanya divalidasi saat konfigurasi disimpan: tahap inilah titik yang
// benar-benar menentukan. Hook ini juga menutup dua cara untuk melewati validasi konfigurasi, yaitu DNS rebinding
// (saat validasi resolve ke IP publik, saat koneksi sungguhan resolve ke jaringan internal) dan redirect (meskipun
// kita sudah menolak lompatan antar-host, lompatan di host yang sama masih bisa mengarahkan path ke tempat lain).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("tidak bisa mengurai alamat tujuan %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("pengiriman ke alamat lokal/link-local %s ditolak (jika memang perlu mengirim ke layanan lokal, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport hanya menambahkan satu guard dial di atas Transport default.
// Memakai Clone agar seluruh tuning default tetap terjaga (connection pool, HTTP/2, timeout, proxy, dll),
// sehingga menambah satu pemeriksaan tidak mengubah perilaku lain.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient adalah klien yang dipakai bersama oleh semua pengiriman channel.
//
// Sengaja **tidak** memakai ulang proxy keluar global milik proyek (GlobalProxy di sisi server): proxy itu dipakai
// untuk trafik target pentest, biasanya berupa tunnel yang tidak stabil, sementara ketersediaan notifikasi tidak
// seharusnya tersandera oleh gangguan jaringan target. Pengiriman IM cukup koneksi langsung. Timeout diset 15 detik,
// karena jika lawan bicara lebih lambat dari itu, sebenarnya sudah dianggap bermasalah.
//
// Menolak redirect antar-host: alamat pengiriman pada fitur ini selalu berbentuk "satu endpoint tetap", sehingga
// normalnya tidak akan diarahkan ke host lain; sementara kredensial pada beberapa platform (access_token DingTalk,
// key WeCom, bot token Telegram) **ada di dalam URL itu sendiri**, sehingga mengikuti redirect antar-host sama
// dengan menyerahkan kredensial ke tujuan redirect. Redirect di host yang sama (misalnya penambahan garis miring
// di akhir) tetap diizinkan.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("terlalu banyak redirect")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("redirect antar-host ditolak (%s -> %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit membatasi ukuran body respons yang dibaca. Saat lawan bicara bermasalah, bisa saja mengembalikan
// konten yang sangat besar, padahal kita hanya butuh kode error dan sedikit deskripsi untuk ditampilkan di riwayat pengiriman.
const respBodyLimit = 8 << 10

// doJSON mengirim satu permintaan dan mengembalikan body respons (yang sudah dibatasi panjangnya).
//
// Saat payload nil, body yang dikirim kosong (untuk GET atau platform yang tidak membutuhkan body).
// Key-value di headers ditambahkan apa adanya, dipakai untuk header kustom pada webhook generik.
//
// Klasifikasi error adalah tanggung jawab utama fungsi ini: kegagalan di level jaringan serta 5xx/408/429
// dikategorikan "bisa dicoba ulang", sisanya 4xx dikategorikan "gagal permanen", karena mencoba ulang error 403
// hanya akan menulis error yang sama ke log tiga kali.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// Kegagalan serialisasi adalah bug lokal (tipe field konfigurasi salah), dicoba ulang pun tidak akan membantu.
			return nil, Permanent(fmt.Errorf("gagal membuat body permintaan: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// URL tidak valid, kemungkinan besar pengguna salah mengisi alamat, ini termasuk gagal permanen.
		// Di sini juga tidak boleh meneruskan err apa adanya: teks error dari url.Parse memuat alamat lengkap.
		return nil, Permanent(fmt.Errorf("alamat permintaan tidak valid: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Koneksi ditolak, DNS gagal, timeout, kebanyakan adalah gangguan sementara, jadi diserahkan ke mekanisme retry dengan backoff.
		//
		// Teks error harus disamarkan dulu sebelum diteruskan keluar. Alasannya: http.Client.Do mengembalikan *url.Error,
		// dan Error()-nya berbentuk `Op "URL lengkap": error dasar`, sementara kredensial pada beberapa platform ini
		// **ada di dalam URL itu sendiri** (access_token DingTalk, key WeCom, hook id Feishu, /bot<token>/ Telegram).
		// Jika tidak disamarkan, kredensial akan ikut mengalir lewat string error ini ke empat tempat: kolom last_error
		// di notification_deliveries (tersimpan sebagai teks biasa), respons endpoint riwayat pengiriman (**melewati
		// masking pada konfigurasi channel**), log server, serta teks 502 yang dikembalikan endpoint kirim-uji ke frontend.
		return nil, fmt.Errorf("permintaan gagal: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("gagal membaca respons: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (rate limit) dan 408 (timeout) layak dicoba ulang; sisanya 4xx adalah masalah konfigurasi atau izin, dicoba ulang tidak ada gunanya.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("pihak lain membatasi laju atau timeout (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("layanan pihak lain bermasalah (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("permintaan ditolak pihak lain (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet memadatkan body respons menjadi satu baris teks singkat untuk pesan error. Respons bisa memuat baris baru
// dan banyak whitespace, dan jika langsung dimasukkan ke last_error akan merusak tampilan halaman riwayat pengiriman.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget memadatkan alamat pengiriman menjadi bentuk "scheme://host/…" untuk pesan error.
//
// Ini adalah satu-satunya mekanisme penyamaran alamat di paket ini, dan sengaja dibuat **cukup kasar**: selain
// scheme dan host, semuanya dibuang. Alasannya, tidak ada cara "umum dan aman" untuk menentukan bagian mana dari
// URL yang merupakan kredensial:
//
//	DingTalk  kredensial di query      /robot/send?access_token=xxx
//	WeCom     kredensial di query      /cgi-bin/webhook/send?key=xxx
//	Feishu    kredensial di **akhir path**  /open-apis/bot/v2/hook/<hook_id>
//	Telegram  kredensial di **tengah path**  /bot<token>/sendMessage
//
// Kalau ingin "hanya menyisakan bagian yang berguna", harus dibuat patch per channel, dan melewatkan satu platform
// saja berarti satu kebocoran kredensial. Menyisakan host saja sudah cukup untuk investigasi (DNS tidak bisa
// di-resolve, tidak bisa connect, sertifikat salah semuanya bisa dilacak), sementara bot mana yang dimaksud bisa
// dikenali dari petunjuk digit akhir yang disamarkan pada konfigurasi channel.
//
// Saat gagal diparse, mengembalikan placeholder tetap, tidak pernah menampilkan kembali string aslinya.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(alamat tidak dapat diurai)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError melepas alamat dari error pada level transport, hanya menyisakan alasan dasarnya.
//
// Struktur *url.Error adalah {Op, URL, Err}, dan Error()-nya menampilkan URL sekalian. Di sini field Err diambil
// secara eksplisit, melewati Error()-nya, karena ini lebih dapat diandalkan dibanding melakukan penggantian string
// setelahnya, sebab penggantian harus menangani berbagai variasi hasil encoding/escaping URL dengan benar, dan mudah terlewat.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: error tidak dikenal", uerr.Op, host)
	}
	// Error yang bukan *url.Error (misalnya error dari kebijakan redirect) juga bisa membawa alamat, disamarkan dengan cara yang sama.
	return redactURLsInText(err.Error())
}

// redactURLsInText mengganti alamat http(s) yang muncul dalam sebuah teks dengan bentuk yang sudah disamarkan.
//
// Dipakai sebagai jaring pengaman untuk error yang tidak punya field terstruktur (error kebijakan redirect, error
// kustom dari library pihak ketiga). Hanya mengenali prefiks http/https, dipotong berdasarkan whitespace dan tanda
// kutip, karena alamat tidak akan mengandung dua jenis karakter tersebut.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
