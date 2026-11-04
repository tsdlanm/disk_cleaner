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

Halaman utama punya tiga halaman: **Drive**, **File baru**, dan **Recycle Bin**. Tekan `Tab` untuk pindah halaman
(`Shift+Tab` mundur).

- **Drive**: pemakaian dan ruang bebas tiap drive. `Enter` membuka drive.
- **File baru** (halaman sendiri, di antara Drive dan Recycle Bin; `Esc` kembali ke Drive): folder yang kemasukan
  file baru, per drive, dari yang terbaru. **Rentangnya bisa dipilih: 24 jam (bawaan) atau 7 hari**: `t` berganti,
  atau `1` untuk 24 jam dan `7` untuk 7 hari. Pemindaian selalu mengumpulkan 7 hari, jadi berganti rentang
  langsung tanpa memindai ulang (hasil tersimpan dari versi lama yang hanya memuat 24 jam dipindai ulang sekali).
  Rentang yang dipilih juga dipakai untuk penanda hijau `● baru` di explorer, dan kembali ke 24 jam saat
  aplikasi dibuka lagi. File dikelompokkan ke folder yang
  bermakna (`~\AppData\Local\Google\Chrome`, `C:\Program Files (x86)\Microsoft\Copilot`, `D:\anam\scan_cli`), dan
  cache, aplikasi, maupun file milikmu tidak dibedakan, karena semuanya memakai ruang. `~` adalah folder profilmu.
  Daftar ini **diperbarui langsung** selama aplikasi terbuka (tanda `● langsung`).

  ```
  Drive  │  File baru  │  Recycle Bin    tab pindah halaman
  File baru   24 jam  7 hari  · 913 file · 4.9 GB · dari yang terbaru · dipindai 1 jam lalu
   C:\Program Files (x86)\Microsoft\Edge\      ← path lengkap baris di kursor
   ▾ C:\ OS · 902 file baru · 16 folder · 4.1 GB
      go-build\                     53 file   126.8 MB  1 menit lalu
      Edge\                        197 file   842.2 MB  1 jam lalu
      scan-windows-amd64 (1).zip              3.3 MB  21 jam lalu
  ```
  - Tiap baris: nama folder terluarnya (biru), atau nama file jika isinya hanya satu file; lalu jumlah file baru,
    ukurannya, dan kapan file terbarunya dibuat. **Path lengkapnya tampil di baris paling atas bagian ini saat kursor
    di baris itu**, jadi folder bernama sama (misalnya dua `Edge`) tetap bisa dibedakan.
  - **Judul drive bisa dipilih**: `Enter` di sana menutup semua isinya sampai tinggal judul drive (`▸ C:\ OS · …`),
    `Enter` lagi membukanya (`▾`). `←` dari folder mana pun menutup drive-nya, `→` membukanya lagi.
  - Tiap drive menampilkan **10 folder** teratas; `▸ tampilkan lainnya (N folder lagi)` di bawahnya (pilih, lalu
    `Enter`) menambah 10 folder setiap kali.
  - `Enter` masuk ke folder itu di aplikasi, dengan kursor di subfolder/file berisi file terbarunya (`Esc` kembali).
  - `a` menganalisis folder itu dengan AI, `o` membukanya di Explorer.
- **Recycle Bin**: isi Recycle Bin dari semua drive lokal, dari yang terbesar, beserta kapan dihapus. Daftar hanya
  memuat nama file/foldernya; **path lengkap lokasi asal item di kursor tampil di baris paling atas**.
  - **`✕ Kosongkan Recycle Bin`** adalah baris pertama (tekan `↑` dari item pertama, atau `g`): `Enter` di sana
    menghapus permanen seluruh isinya sekaligus (ada konfirmasi `y`). `E` melakukan hal yang sama dari mana pun.
  - `Space` menandai item, `u` / `Enter` memulihkan ke lokasi asalnya, `d` menghapus permanen (ada konfirmasi).
    Tanpa tanda, aksi berlaku untuk item di kursor. `o` membuka Recycle Bin di Windows Explorer.

## Tombol

| Tombol | Fungsi |
|---|---|
| `↑` `↓` | Pindah pilihan |
| `g` / `Home` | Lompat ke baris pertama (di semua daftar) |
| `G` / `End` | Lompat ke baris terakhir |
| `PgUp` `PgDn` | Naik / turun satu layar |
| `Enter` | Buka drive / folder |
| `←` / `Backspace` | Kembali ke folder sebelumnya |
| `Esc` | Kembali ke halaman utama |
| `Tab` / `Shift+Tab` | Di halaman utama: pindah halaman Drive → File baru → Recycle Bin |
| `Space` | Tandai item (bisa banyak) |
| `d` / `Delete` | Hapus item ke Recycle Bin (ada konfirmasi) |
| `r` | Hitung ulang (di halaman utama: pindai ulang semua drive) |
| `t` / `1` / `7` | Di File baru: ganti rentang 24 jam ↔ 7 hari, atau pilih langsung |
| `o` | Buka di Windows Explorer |
| `a` | Pindai folder yang dipilih lalu tanya dulu sebelum dikirim ke AI (analisis + chat) |
| `p` | Pengaturan AI |
| `A` | Restart sebagai Administrator (agar folder terkunci ikut terhitung) |
| `q` | Keluar |

Di dalam folder, file yang dibuat dalam rentang File baru (24 jam atau 7 hari) ditandai hijau **`● baru · 14:05 · 2 jam lalu`**
(untuk yang lebih dari sehari cukup `● baru · 3 hari lalu`), dan subfolder
yang berisi file baru diberi label **`N file baru`**.

## Analisis AI dan chat

1. Tekan `p`, pilih provider (Gemini punya tier gratis, buat key di [aistudio.google.com/apikey](https://aistudio.google.com/apikey)),
   pilih model, lalu tempel API key.
2. Pilih folder atau drive, lalu tekan `a`. Aplikasi hanya memindai foldernya di komputermu (tanpa AI, tanpa
   token) dan menampilkan ringkasan: jumlah file, ukuran, dan perkiraan token yang akan dikirim. **Tidak ada yang
   dikirim ke AI sebelum kamu setuju**: `Enter` mengirim, `v` melihat data yang akan dikirim, `Esc` membatalkan.
3. **Hasil analisis** dimulai dari ringkasan, lalu setiap item besar dinilai satu per satu:

   ```
   › Analisis C:\Users\anam\.vscode

   ● Folder ini menyimpan ekstensi dan pengaturan Visual Studio Code. Ruang paling banyak dipakai ekstensi AI.

     ✕ Aman dihapus · 3 item
       extensions\
       ▸ anthropic.claude-code-2.1.312\                    296.9 MB
       ▸ ms-toolsai.jupyter-2025.8.0\                       17.2 MB
       ▸ ms-toolsai.jupyter-2025.7.0\                       17.2 MB
         Versi lama; sudah ada versi yang lebih baru.

     ? Perlu dicek dulu · 1 item
       ▸ extensions\openai.chatgpt-26.1021.22027\             1.5 GB
         Ekstensi ChatGPT yang terpasang; hapus lewat VS Code jika tidak dipakai.
   ╭──────────────────────────────────────────────────────────────╮
   │ › ketik pertanyaan, atau / untuk perintah                    │
   ╰──────────────────────────────────────────────────────────────╯
   ```

   - **Aplikasi yang memilih item yang dinilai**, bukan AI: item terbesar di folder itu (paling banyak 40, yang
     sangat kecil dilewati). Folder wadah seperti `AppData\Local`, `Google`, atau `.vscode\extensions` dibuka
     dulu, jadi yang dinilai adalah satu aplikasi, ekstensi, atau cache, bukan "AppData" saja.
   - **Setiap item pasti dinilai.** AI menjawab dengan format tetap (ringkasan + saran per item); item yang
     terlewat ditanyakan ulang sekali, dan yang tetap terlewat masuk "Perlu dicek dulu" dengan keterangan itu.
     Susunan dan format layar dibuat aplikasi, jadi tidak bergantung pada kerapian jawaban AI.
   - **Fakta dihitung aplikasi** dan ikut dikirim: kapan terakhir diubah, jumlah file, dan versi dibanding item
     sejenis ("ada versi lebih baru", "satu-satunya versi"). AI tidak perlu menebak mana versi lama.
   - Kelompoknya: **Aman dihapus** (seluruh isinya dibuat ulang otomatis), **Perlu dicek dulu** (bisa berisi data,
     program, atau AI tidak yakin), **Sebaiknya disimpan**. Di tiap kelompok, dari yang terbesar. Saran AI tetap
     tebakan dari nama, ukuran, dan tanggal; baca alasannya sebelum menghapus.
   - **Tanya lanjutan**: ketik pertanyaan, `Enter` kirim. AI sudah memegang ringkasan folder dan jawabannya
     sendiri, jadi pertanyaan seperti "kenapa ini aman dihapus?" tidak perlu menjelaskan ulang.
   - **AI bisa melihat lebih dalam**, hanya membaca: `Melihat isi <folder>` (nama, ukuran, tanggal) dan
     `Mencari file` (menurut ukuran, umur, atau nama). Langkahnya tampil di percakapan. Alat hanya bekerja di
     dalam folder yang dianalisis, isi file tidak pernah dibaca, dan AI tidak punya cara menghapus apa pun.
   - **Item di hasil** tampil seperti di explorer: folder biru, ukuran rata kanan, alasan AI redup di bawahnya.
     Folder induk yang sama ditulis sekali di bawah judul, akhiran platform (`-win32-x64`) disembunyikan, dan alasan
     yang sama untuk item berurutan ditulis sekali; path lengkap tampil di baris status saat item dipilih. Tekan
     `Tab` untuk memilihnya: `↑` `↓` pindah, `Enter` membuka isi folder di bawah butirnya, `Space` menandai, `d`
     menghapus (ke Recycle Bin, ada konfirmasi), `e` membuka di explorer aplikasi, `o` di Windows Explorer. Baris
     di atas kotak ketik menunjukkan saran (✕ aman, ? cek, ● simpan) dan alasan item terpilih. `Tab` atau `Esc`
     kembali mengetik.   - **Perintah** (ketik di kotak): `/data` melihat data folder yang dikirim ke AI, `/ulang` memindai ulang (lalu bertanya lagi sebelum mengirim),
     `/bersih` menghapus pertanyaan lanjutan, `/model` mengganti provider/model/API key, `/bantuan`.
   - `↑` `↓` di kotak ketik mengulang pertanyaan sebelumnya, `PgUp` `PgDn` menggulir, `Esc` membatalkan jawaban
     yang ditunggu atau menutup layar. **Percakapan tidak disimpan**: menutup layar (atau membuka item di explorer
     dengan `e`) membuangnya, dan `a` berikutnya mulai dari awal.

Yang dikirim ke AI hanya nama folder/file, ukuran, jumlah file, dan tanggal, tidak pernah isi file.

### Token

Setiap pertanyaan lanjutan mengirim ulang riwayat percakapan (termasuk ringkasan folder), jadi satu pertanyaan
bisa memakai puluhan ribu token pada folder besar. Header menampilkan jumlah pertanyaan dan total token. Beberapa
cara menghematnya: Gemini otomatis memakai cache untuk awalan yang sama; `/bersih` membuang riwayat panjang;
hasil alat dipotong maksimal 6.000 karakter; satu pertanyaan paling banyak 6 putaran alat.

## Catatan

- Penghapusan selalu ke **Recycle Bin**; ruang drive baru benar-benar bebas setelah Recycle Bin dikosongkan.
- Pemindaian pertama butuh beberapa puluh detik. Setelah itu hasilnya disimpan, jadi membuka aplikasi lagi langsung tampil.
- Pengaturan tersimpan di `%APPDATA%\scan\config.json`. `recent_min_kb` mengatur ukuran file terkecil yang
  dihitung sebagai file baru (bawaan 1024 KB).
