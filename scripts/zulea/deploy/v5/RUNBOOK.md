# Pakiet v5 — runbook wdrożeniowca (0.6.1, tylko §3)

Wersja programu: **0.6.1**. Obraz do zbudowania: `zulea-upbrr:0.6.1`.
Punkt powrotu, którego nie ruszamy: obraz `zulea-upbrr:5b87372`.

Okno: bez remuxu, TM, BBCode i movera. Wszystko tylko odczyt, poza katalogiem
`work` uploadera. Ten runbook kończy się raportem. Nie idzie dalej.

## Czego nie ruszać

Zostaje wrapper wdrożony dziś na hoście (utwardzenie z audytu: `--read-only`,
`--tmpfs /tmp`, `--cap-drop ALL`, `no-new-privileges`, montowania przez
`sync-inputs`). **Nie podmieniać całego wrappera** i nie podmieniać `nightly.sh`.
Ta paczka tych skryptów nie zawiera.

Jedyna zmiana we wrapperze to linia z tagiem obrazu:

```
IMAGE=zulea-upbrr:0.6.1
```

Przed edycją i po niej zapisz SHA wrappera. Różnica ma być tylko w tej linii.
Obraz `zulea-upbrr:5b87372` zostaje na hoście jako powrót.

## 1. Obraz

Weź `MEDIAINFO_VERSION` z budowy obrazu `zulea-upbrr:5b87372` (ta sama wartość
co dziś; MediaInfoLib 26.01). Nie zmieniaj jej.

```bash
docker build -f scripts/zulea/docker/Dockerfile \
  --build-arg MEDIAINFO_VERSION=<ta sama co dziś> \
  -t zulea-upbrr:0.6.1 .
docker run --rm --entrypoint cat zulea-upbrr:0.6.1 /usr/local/share/zulea-upbrr/BUILD-INFO
docker run --rm --network none --entrypoint zulea-upbrr zulea-upbrr:0.6.1 version
```

Oczekiwane: `version` wypisuje `zulea-upbrr 0.6.1`. `BUILD-INFO` wpisz do raportu.

## 2. check-config

Tym samym wrapperem, po zmianie wyłącznie `IMAGE=`:

```bash
skrypty/zulea-upbrr.sh --network-none check-config --config /secrets/zulea.json
```

Oczekiwane: kod wyjścia **0**.

## 3. Mama, tylko odczyt

```bash
skrypty/zulea-upbrr.sh --network-none verify-identity --config /secrets/zulea.json --release Mama.2013.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea
skrypty/zulea-upbrr.sh --network-none check --config /secrets/zulea.json --release Mama.2013.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea
```

Oczekiwane: tożsamość zgodna (wariant A), łańcuch OK. Wynik do raportu.
Biblioteka zostaje tylko do odczytu.

## 4. Raport i STOP

Raport: `version` = 0.6.1, `BUILD-INFO`, SHA wrappera przed i po (różnica tylko
w linii `IMAGE=`), `check-config` = 0, wynik `verify-identity` i `check`.
`nightly.sh` bez zmian.

**STOP.** Nie uruchamiaj `dupe-accept`, `plan`, `upload`, `resolve` ani `seed`.
