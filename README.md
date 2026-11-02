# SCH Repository Version Updater

Scheduler Go untuk memperbarui versi runtime atau Docker image pada repository Bitbucket Cloud maupun Bitbucket Server/Data Center melalui HTTPS. Service mengambil job SQL Server, melakukan atomic claim, bekerja pada branch baru, memvalidasi satu-satunya file yang berubah, lalu commit dan push. Service tidak memanggil Bamboo dan tidak membuat pull request.

## Alur status

| Kondisi | `is_active` | `flag_done` | `job_status` |
|---|---:|---:|---|
| Siap diproses | 1 | 0 | status sebelumnya atau `PENDING` |
| Sudah diklaim | 1 | 1 | `RUNNING` |
| Gagal | 1 | 2 | `FAILED` |
| Berhasil | tidak diubah | 3 | `SUCCESS` |

Claim merupakan satu conditional `UPDATE`; hanya worker yang mendapat tepat satu affected row yang boleh memproses job. Scheduler hanya mengambil row dengan `is_active = 1`, `flag_done = 0`, dan `target_version` yang sudah diisi (bukan `NULL`, kosong, atau hanya spasi), lalu `last_run_at` diisi saat claim. Service tidak pernah mengubah `is_active`; aktivasi dan deaktivasi sepenuhnya dikendalikan user melalui database. Saat sukses, transisi database secara atomik menyalin versi target ke `current_version`, mengosongkan `target_version` menjadi `NULL`, lalu menetapkan flag `3` dan status `SUCCESS`.

Job `RUNNING` yang lebih tua dari `RUNNING_STALE_AFTER_SECONDS` dipulihkan menjadi `FAILED` dengan `flag_done = 2`, tanpa mengubah `is_active`. Nilai stale wajib lebih besar dari timeout job agar scheduler tidak mengambil alih worker yang masih berada dalam batas waktu. Job gagal maupun sukses hanya dijalankan ulang setelah user mengubah `flag_done` kembali menjadi `0`; `job_status` akan otomatis menjadi `RUNNING` saat claim berikutnya.

Untuk rerun manual, isi `target_version` baru sekaligus mengubah flag: `UPDATE dbo.VersionUpdateJob SET target_version = @target, flag_done = 0 WHERE id = @id AND flag_done IN (2,3)`. Jangan mengubah flag job yang masih `RUNNING` (`flag_done = 1`) karena dapat menyebabkan eksekusi paralel atas job yang sama.

Jika file sudah berisi versi target (`NO_CHANGE`), dependency validation tetap berjalan. Bila remediation mengubah manifest/lockfile, perubahan tersebut tetap di-commit dan di-push; bila tidak ada perubahan, job diselesaikan sebagai `SUCCESS` tanpa commit kosong.

## Target yang didukung

- `GO`: directive `go` pada `go.mod`. Menjalankan `go test -p=1` untuk membatasi penggunaan temporary disk, lalu `govulncheck`; modul dengan vulnerability reachable di-update hanya ke fixed version yang dilaporkan, kemudian `go mod tidy`, test, dan scan diulang.
- `JAVA`: properti `java.version` dan/atau `maven.compiler.release` pada XML. Konfigurasi Java lain ditolak agar tidak ada perubahan spekulatif.
- `NODEJS`: file versi eksplisit seperti `.nvmrc`; newline akhir dipertahankan.
- `DOCKER_IMAGE`: semua instruksi `FROM` yang image name-nya sama dengan `image_name`; registry, path, opsi platform, stage alias, dan baris lain dipertahankan. Hanya penggantian tag dan validasi sintaks Dockerfile yang dilakukan; image tidak dipindai atau diuji.

Dependency validation berlaku pada target runtime/package berikut:

- Go memakai `govulncheck` pinned dan hanya me-remediasi modul reachable dengan fixed version yang jelas. Scan, upgrade, tidy, dan test diulang hingga bersih dengan batas lima putaran. Jika beberapa temuan atau dependency meminta versi berbeda untuk modul yang sama, versi tertinggi yang kompatibel dipilih. Vulnerability standard library harus diselesaikan oleh `target_version`/toolchain dan tidak ditebak sebagai module update.
- Node.js memakai `npm audit`, menjalankan non-breaking `npm audit fix --package-lock-only`, lalu `npm ci` dan test. Fix yang membutuhkan `--force` ditolak.
- Java menjalankan Maven test dan OWASP Dependency-Check. Database OWASP tidak menyediakan mapping fixed Maven version yang selalu deterministik, sehingga temuan yang tidak dapat dipetakan dengan aman membuat job gagal dan tidak memicu upgrade massal.
- Docker image tidak menjalankan dependency validation, pull image, atau vulnerability scan; updater hanya mengganti tag yang diminta secara eksplisit.

Perubahan otomatis dibatasi ke target file serta manifest/lockfile ekosistem (`go.mod`, `go.sum`, `package.json`, atau `package-lock.json`). File lain menyebabkan job gagal sebelum commit.

Gradle belum didukung karena tidak ada konfigurasi target yang membedakan bentuk deklarasinya. Job dengan format tersebut gagal dengan pesan yang jelas.

## Konfigurasi

Salin `.env.example` menjadi `.env` untuk local development, atau gunakan secret/configuration mechanism deployment. Binary memuat `.env` bila file tersebut tersedia; environment yang sudah diekspor tetap memiliki prioritas. Environment wajib: `DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, dan `BITBUCKET_API_TOKEN_PATH`.

`BITBUCKET_API_TOKEN_PATH` harus menunjuk file yang hanya berisi personal access token service account, dengan permission tanpa akses group/other (contoh `0600`). Token dikirim sebagai Bearer header untuk operasi Git dan REST API, serta tidak ditulis ke URL, database, source, atau log. Prompt interaktif Git dimatikan.

`repo_url` hanya menerima clone URL HTTPS tanpa kredensial, query, fragment, atau whitespace, misalnya `https://bitbucket.example/scm/project/repository.git`. URL HTTP, SSH, dan password/token di dalam URL ditolak.

## Automatic repository discovery

Isi `BITBUCKET_PROJECT_URL` dengan URL halaman project, misalnya `https://bitbucket.example/projects/CASHREOPSC`, untuk mengaktifkan discovery. `BITBUCKET_API_TOKEN_PATH` menunjuk file secret mode `0600` yang hanya berisi personal access token service account; token dipakai sebagai Bearer token dan tidak pernah ditulis ke log/database. Saat startup, discovery membaca `MAX(created_at)` dari tabel job. Jika tanggal terbaru belum berumur 30 hari, scan ditunda sampai genap 30 hari; tabel kosong atau tanggal yang sudah jatuh tempo akan langsung dipindai. Setelah scan berjalan, scan berikutnya dijadwalkan setiap 30 hari secara default (`DISCOVERY_INTERVAL=30d`). Satuan yang didukung adalah hari (`d`), jam (`h`), menit (`m`), dan detik (`s`), termasuk kombinasi seperti `1d12h30m`. `DISCOVERY_INTERVAL_SECONDS` lama masih diterima sebagai fallback bila `DISCOVERY_INTERVAL` tidak diisi.

Setiap repository dari REST API di-clone melalui HTTPS pada branch tetap `release/development` ke workspace unik. Repository tanpa branch tersebut atau tanpa target yang didukung dilewati tanpa menghentikan repository lain. Deteksi memilih satu target utama dengan prioritas `go.mod`, `pom.xml`, `.nvmrc`, lalu `Dockerfile`; kandidat terdangkal dipilih bila ada beberapa file sejenis.

Row hasil discovery dibuat idempotent berdasarkan kombinasi repository, branch, file, dan target type:

- `product_id` dan `product_name`: project key/name dari Bitbucket.
- `job_name`: nama repository.
- `job_type`: `VERSION_UPDATE`.
- `repo_url`: HTTPS clone URL dari Bitbucket.
- `branch_name`: `release/development`.
- `target_type`, `file_path`, `image_name`, dan `current_version`: hasil inspeksi source.
- `target_version`: `NULL`.
- `is_active = 0`, `flag_done = 0`, `job_status = PENDING`.

User kemudian mengisi `target_version` dan mengubah `is_active = 1` untuk mengaktifkan job. Jika hanya `is_active` yang diubah sedangkan target masih kosong, scheduler akan membiarkan job tetap `PENDING` dengan `flag_done = 0`.

Tidak ada `WORKSPACE_ROOT`. Implementasi memakai temporary root milik runtime (`os.TempDir`) dan membuat direktori unik `version-job-<id>-<random>` untuk setiap eksekusi. Hanya subdirektori tersebut yang dihapus melalui deferred cleanup; root dan workspace job lain tidak pernah dihapus.

## Instalasi dan penggunaan

1. Jalankan [`migrations/001_create_version_update_job.sql`](migrations/001_create_version_update_job.sql) untuk instalasi baru. Pada tabel existing, jalankan migration secara berurutan termasuk [`migrations/002_allow_clear_target_version.sql`](migrations/002_allow_clear_target_version.sql).
2. Sediakan Git, Go + `govulncheck`, Node.js/npm, dan Maven/Java sesuai jenis job yang dijalankan. Mount file personal access token mode `0600`, lalu isi environment dari [`.env.example`](.env.example).
3. Build dan jalankan:

   ```sh
   go build -o bin/sch-version-updater ./cmd/sch
   ./bin/sch-version-updater
   ```

   Atau build image menggunakan `Dockerfile`; container berjalan sebagai user non-root.

Contoh job:

```sql
INSERT dbo.VersionUpdateJob
    (product_id, product_name, job_name, job_type, repo_url, branch_name,
     file_path, target_type, image_name, current_version, target_version)
VALUES
    ('ATM', 'cashrecon', 'upgrade-go', 'VERSION_UPDATE',
     'https://bitbucket.org/workspace/payments.git', 'main',
     'go.mod', 'GO', NULL, '1.22', '1.23.4');
```

Branch keluaran berbentuk `chore/version-update-<job-id>-<UTC timestamp>`. Protected source branch tidak pernah menjadi tujuan push.

## Validasi dan test

```sh
go test ./...
go vet ./...
```

Test memakai temporary local files, fake Git runner, dan mock SQL; tidak mengakses repository production. Log JSON mencakup job ID, product ID, repository, tahap, branch, durasi, status, dan error tanpa mencetak password atau isi private key.

## Batas operasional

- Pastikan service account Bitbucket hanya memiliki akses repository yang diperlukan.
- Set `RUNNING_STALE_AFTER_SECONDS` di atas `JOB_TIMEOUT_SECONDS`.
- Push dicoba ulang terbatas dengan backoff. Keberhasilan commit lokal tidak dianggap sukses sampai push selesai.
- Karena schema tidak memiliki kolom pesan error, detail kegagalan tersedia di log terstruktur; status database tetap `FAILED` dan dapat dicoba ulang.
