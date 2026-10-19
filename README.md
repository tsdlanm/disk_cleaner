# cli_app

CLI berbasis Go dan [Cobra](https://github.com/spf13/cobra).

## Menjalankan

```sh
go run . --help
go run . hello --name Budi
go run . version
```

## Build

```sh
go build -o bin/cli_app .
```

## Menambah command

Buat file baru di `cmd/`, definisikan `cobra.Command`, lalu daftarkan dengan `rootCmd.AddCommand(...)` di `init()`.
