# scan

Aplikasi terminal untuk Windows yang menunjukkan apa saja yang memakan ruang di drive, lalu membantu kamu
membersihkannya.

## Cara menjalankan

Jalankan `scan.exe` dari terminal (Windows Terminal disarankan).

```sh
scan.exe                # mulai dari daftar drive
scan.exe --path D:\data # langsung buka folder tertentu
```

Belum punya `scan.exe`? Build dulu dengan `go build -o scan.exe .`

## Halaman utama

- **Drive**: pemakaian dan ruang bebas tiap drive.
- **File baru**: folder yang kemasukan file baru dalam 7 hari terakhir, per drive, dari yang paling baru.
  Daftar ini **diperbarui langsung** selama aplikasi terbuka (tanda `● langsung`).
  - Folder milikmu tampil di atas.
  - Cache, aplikasi, dan sistem dirangkum menjadi satu baris `▸ cache, aplikasi & sistem`; tekan `Enter` untuk membukanya.
  - Tekan `Tab` untuk memilih folder di daftar ini.

## Tombol

| Tombol | Fungsi |
|---|---|
| `↑` `↓` | Pindah pilihan |
| `Enter` | Buka drive / folder |
| `←` / `Backspace` | Kembali ke folder sebelumnya |
| `Esc` | Kembali ke halaman utama |
| `Tab` | Pindah ke / dari daftar "File baru" |
| `Space` | Tandai item (bisa banyak) |
| `d` / `Delete` | Hapus item ke Recycle Bin (ada konfirmasi) |
| `s` | Urutkan berdasarkan ukuran / nama |
| `r` | Hitung ulang (di halaman utama: pindai ulang semua drive) |
| `o` | Buka di Windows Explorer |
| `a` | Minta saran AI untuk folder yang dipilih |
| `b` | Buka lagi hasil AI terakhir |
| `p` | Pengaturan AI |
| `A` | Restart sebagai Administrator (agar folder terkunci ikut terhitung) |
| `q` | Keluar |

Di dalam folder, file baru ditandai hijau **`● baru · 2 jam lalu`**, dan subfolder yang berisi file baru diberi
label **`N file baru`**.

## Saran AI

1. Tekan `p`, pilih provider (Gemini punya tier gratis, buat key di [aistudio.google.com/apikey](https://aistudio.google.com/apikey)),
   pilih model, lalu tempel API key.
2. Pilih folder atau drive, lalu tekan `a`.
3. AI menyebutkan folder/file yang bisa dihapus: **merah** = aman dihapus, **kuning** = cek dulu, **hijau** = simpan.
   Pilih dengan `↑` `↓`, `Enter` untuk melihat isinya, `d` untuk menghapus.

Yang dikirim ke AI hanya nama folder/file, ukuran, dan tanggal, tidak pernah isi file.

## Catatan

- Penghapusan selalu ke **Recycle Bin**; ruang drive baru benar-benar bebas setelah Recycle Bin dikosongkan.
- Pemindaian pertama butuh beberapa puluh detik. Setelah itu hasilnya disimpan, jadi membuka aplikasi lagi langsung tampil.
- Pengaturan tersimpan di `%APPDATA%\scan\config.json`. `recent_days` mengatur rentang "File baru" (bawaan 7 hari),
  dan `recent_min_kb` mengatur ukuran file terkecil yang dihitung (bawaan 1024 KB).
