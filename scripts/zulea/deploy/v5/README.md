# Pakiet wdrożeniowy v5

`zulea-upbrr` **0.6.0**. Do przeglądu Kocura. Nie jest wdrożeniem.

| Plik | Rola |
|---|---|
| `RUNBOOK.md` | Kroki §3 dla wdrożeniowca. Koniec runbooka to STOP. |
| `zulea-upbrr.sh` | Wrapper hosta (`--network-none`, bez własnych flag). |
| `zulea-upbrr.env.example` | Ścieżki do uzupełnienia lokalnie. Wypełnionego env nie commitujemy. |
| `nightly.sh` | Szablon nocny bez `dupe-accept`, `upload`, `resolve` i `seed`. |
| `VERSION` | 0.6.0, baseline `5b87372`, obraz powrotu `zulea-upbrr:5b87372`. |
| `SHA256SUMS` | Suma plików pakietu. Sprawdzić przed podmianą. |

Komenda `dupe-accept` jest w binarku 0.6.0. Ten pakiet jej nie uruchamia.
