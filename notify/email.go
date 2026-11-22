package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout masing-masing membatasi waktu membangun koneksi dan seluruh sesi SMTP.
// net/smtp sendiri tidak punya mekanisme timeout apa pun; tanpa kedua batas ini, satu koneksi lawan yang macet akan membuat
// goroutine pengiriman tergantung selamanya di sana, sementara dispatcher memproses secara berurutan dalam satu goroutine,
// sehingga sama dengan melumpuhkan seluruh sistem notifikasi.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel mengimplementasikan pengiriman email lewat SMTP.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email tidak punya rate limit dari platform, tapi jangan dipakai untuk membanjiri; diberi nilai default yang longgar.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Hanya password yang disamarkan. Host SMTP, akun, dan penerima bukan rahasia; menyamarkannya hanya akan menyulitkan proses edit.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port menentukan server mana yang menerima password; tls menentukan apakah transmisinya dienkripsi. Perubahan pada salah satu dari ketiganya
// mengharuskan password dimasukkan ulang, sekaligus membuat langkah "menonaktifkan TLS" harus menyertakan kredensial secara eksplisit, bukan sekadar diubah begitu saja.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("alamat server SMTP belum diisi")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("port SMTP tidak valid (harus 1-65535)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("alamat pengirim belum diisi")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("minimal satu alamat penerima diperlukan")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: ditingkatkan jika didukung lawan. Kredensial tidak boleh dikirim pada sesi plain text (lihat penjelasan auth di bawah).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS gagal: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth akan menolak mengirim kredensial lewat koneksi yang tidak terenkripsi (kecuali tujuannya localhost).
			// Ini adalah perilaku keamanan yang **benar** dan tidak boleh dilewati, tapi alasannya perlu dijelaskan dengan baik,
			// kalau tidak pengguna hanya akan melihat "unencrypted connection" tanpa tahu harus berbuat apa.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("kredensial ditolak: koneksi tidak terenkripsi. Aktifkan TLS, gunakan port 465 (TLS implisit), atau centang \"Aktifkan TLS\" (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("autentikasi SMTP gagal: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("pengirim %s ditolak", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("penerima %s ditolak", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA gagal: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("gagal menulis isi email: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("gagal mengirim email: %w", err)
	}
		// Kegagalan Quit tidak mengubah fakta bahwa email sudah diterima server, jadi errornya diabaikan.
	_ = client.Quit()
		// Email tidak dipotong panjangnya (seluruh isi HTML dikirim), seluruh batch dianggap terkirim.
	return len(m.Items), nil
}

// emailDial membangun koneksi SMTP.
//
// implicitTLS=true memakai cara seperti port 465 ("langsung TLS saat terhubung"); false memakai 25/587 yang terhubung dalam plain text dulu baru
// STARTTLS. Keduanya tidak boleh dicampur: mengirim greeting plain text ke port 465 akan langsung diputus.
//
// Batas waktu sesi diatur sejak **saat koneksi dibangun** (bukan ditambahkan belakangan), karena Client di net/smtp menyimpan koneksi
// yang mendasarinya di field yang tidak diekspor, sehingga tidak bisa diakses dari luar; begitu koneksi diserahkan, satu-satunya jaminan
// adalah deadline yang sudah diatur sebelumnya. Ini juga otomatis mencakup blocking pada fase handshake.
// Control dipasangi blockInternalDial, memakai guard yang sama dengan channel-channel berbasis HTTP. Tanpa itu, SMTP
// jadi satu-satunya celah di seluruh perlindungan SSRF: host diisi 169.254.169.254 atau 127.0.0.1 bisa langsung terhubung,
// dan saat handshake smtp.NewClient gagal, baris balasan dari lawan akan ikut terbungkus dalam error, lalu ditampilkan kembali lewat last_error
// di endpoint riwayat pengiriman, membentuk primitive semi-blind read; perbedaan waktu antara "koneksi ditolak" vs "timeout" juga bisa
// dipakai untuk memindai port. Fase dial adalah titik terakhir yang berlaku, dan juga mencakup DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("gagal terhubung ke server SMTP: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake SMTP gagal: %w", err)
	}
	return client, nil
}

// smtpStageError mengklasifikasikan kegagalan suatu tahap menjadi "bisa dicoba ulang" atau "gagal permanen" berdasarkan kode balasan SMTP.
//
// Alasan harus dibedakan: semantik 4xx dan 5xx pada SMTP sangat berbeda,
//   - 4xx (450 greylisting, 451 error lokal, 452 penyimpanan penuh) adalah penolakan **sementara**,
//     cara yang benar adalah mencoba lagi nanti; terutama greylisting, hampir selalu terjadi di setiap pengiriman pertama.
//   - 5xx (550 pengguna tidak ada, 553 alamat tidak valid) adalah penolakan permanen, percobaan ulang tidak ada gunanya.
//
// Jika semuanya diputuskan gagal permanen, server email yang mengaktifkan greylisting akan membuat **setiap** notifikasi
// jatuh ke failed sejak percobaan pertama, padahal kasus seperti inilah yang paling membutuhkan retry otomatis.
// Kode balasan diambil dari tiga digit pertama teks error; jika tidak bisa diambil, diperlakukan sebagai bisa dicoba ulang (lebih baik
// mencoba sekali lagi daripada langsung menganggap gagal permanen hanya karena gagal diparsing).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode mengambil tiga digit kode balasan di awal teks error SMTP, mengembalikan 0 jika tidak ditemukan.
// net/smtp tidak mengekspor field kode error, sehingga harus diambil dari teks; formatnya "450 4.7.1 ...".
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage menyusun email RFC 5322 yang lengkap.
//
// Isi email dikodekan base64 karena dua alasan: pertama, SMTP mewajibkan satu baris tidak lebih dari 1000 byte, sedangkan isi HTML
// (terutama email ringkasan) sangat mudah menghasilkan baris yang terlalu panjang; kedua, base64 secara alami tidak pernah menghasilkan baris
// yang diawali ".", sehingga menghindari masalah SMTP dot-escaping.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
		// Subjek berisi karakter non-ASCII harus dienkode RFC 2047, kalau tidak akan ditampilkan sebagai karakter rusak oleh klien email.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
		// Email tidak punya batas panjang mutlak, sehingga isinya tidak dipotong.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
		// base64 dilipat setiap 76 karakter, sesuai RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
