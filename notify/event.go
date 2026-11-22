package notify

// Snapshot adalah kontrak untuk kolom JSONB notification_events.snapshot. Yang menulis
// adalah transaksi penyimpanan temuan di lapisan db, yang membaca adalah mesin pengiriman
// dan pencocokan filter di lapisan server. Didefinisikan di package ini karena ini adalah
// payload "domain notifikasi": db hanya bertugas serialisasi, tidak memahami arti field.
//
// Alasan menyimpan field temuan secara redundan dan tidak mengambil ulang saat render:
// temuan bisa saja diganti nama, diubah level, atau diubah statusnya kemudian, sementara
// isi notifikasi seharusnya mencerminkan kesimpulan **pada saat kejadian**. Mengambil ulang
// data berisiko memberi kesan keliru seperti "ternyata sudah diubah jadi low belakangan".
// Selain itu fan-out dan rendering jadi tidak perlu JOIN ke tiga tabel findings/tasks/assets.
type Snapshot struct {
	// Jenis event: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Hanya terisi saat kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item adalah satu temuan yang akan dikirim notifikasi, untuk dirender oleh channel.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets adalah nama tampilan aset yang sudah diresolve (misalnya domain/IP).
	// Diisi oleh lapisan server, karena package ini tidak menyentuh database
	// sehingga tidak bisa mengambil namanya sendiri.
	Assets []string
	// DetailURL adalah tautan balik ke detail temuan; kosong berarti public_base_url
	// belum dikonfigurasi, sehingga saat render bagian ini dilewati.
	DetailURL string
	// Khusus untuk event perubahan status; kedua field terisi akan dirender
	// menjadi bentuk "menunggu → sudah diperbaiki".
	FromStatus string
	ToStatus   string
}

// IsStatusChange melaporkan apakah item ini merupakan event perubahan status.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title mengembalikan judul tampilan item: mengutamakan name yang diberi nama manual,
// lalu jatuh ke jenis temuan (vulnclass), dan jika keduanya kosong dipakai placeholder
// supaya judul tidak pernah tampil kosong.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(temuan tanpa nama)"
}

// Message adalah seluruh isi satu kali pengiriman melalui channel.
type Message struct {
	// Panjang 1 untuk notifikasi satuan; untuk notifikasi ringkasan (digest)
	// berisi satu batch penuh. Slice kosong tidak valid, pemanggil wajib
	// memastikan minimal satu item.
	Items []Item
	// Batch=true berarti dirender sebagai pesan ringkasan (judul berbeda,
	// disertai rentang waktu dan jumlah item).
	Batch bool
	// WindowMinutes adalah periode ringkasan (dalam menit), hanya dipakai saat
	// Batch=true untuk teks seperti "dalam N menit terakhir". Nilai ini sengaja
	// diberikan secara eksplisit dari konfigurasi, bukan dihitung dengan
	// time.Since saat render, agar hasil render tetap deterministik dan mudah diuji.
	WindowMinutes int
	// HomeURL adalah alamat panel platform (public_base_url global); jika kosong,
	// tautan ke panel tidak disertakan.
	HomeURL string
}
