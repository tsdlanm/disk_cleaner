# scan

TUI file explorer untuk Windows yang menampilkan ukuran folder, supaya cepat ketahuan apa yang memakan ruang di drive.

- Layar awal: daftar drive beserta pemakaian dan ruang bebasnya.
- Isi folder langsung tampil; ukuran subfolder dihitung paralel di background dan diurutkan dari yang terbesar.
- Hasil hitung disimpan di cache (termasuk semua folder turunan), jadi masuk ke subfolder atau kembali ke folder sebelumnya langsung tampil.

## Menjalankan

```sh
go run .
go build -o scan.exe .
```

## Tombol

| Tombol | Aksi |
|---|---|
| `↑` `↓` / `j` `k`, `PgUp` `PgDn`, `Home` `End` | Pindah pilihan |
| `Enter` / `→` / `l` | Buka drive atau folder |
| `Backspace` / `←` / `h` / `Esc` | Kembali ke folder induk |
| `Space` | Tandai / lepas tanda item (bisa banyak) |
| `Delete` / `d` | Pindahkan item yang ditandai (atau item di kursor) ke Recycle Bin, dengan konfirmasi |
| `s` | Ganti urutan: ukuran / nama |
| `r` | Hitung ulang folder ini |
| `o` | Buka di Windows Explorer |
| `a` | Popup analisis AI untuk item yang dipilih (belum aktif) |
| `A` | Restart sebagai Administrator agar folder terkunci ikut terhitung |
| `q` / `Ctrl+C` | Keluar |

## Struktur

- `internal/ui`: model dan tampilan TUI (Bubble Tea)
- `internal/sizer`: penghitung ukuran folder di background + cache
- `internal/drives`: daftar drive Windows dan kapasitasnya
- `internal/format`: format ukuran dan angka
