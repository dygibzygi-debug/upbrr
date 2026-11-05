# Pakiet v5 — runbook wdrożeniowca (tylko §3)

Wersja programu: **0.6.0**. Obraz do zbudowania: `zulea-upbrr:0.6.0`.
Punkt powrotu, którego nie ruszamy: obraz `zulea-upbrr:5b87372`.

Okno: bez remuxu, TM, BBCode i movera. Wszystko tylko odczyt, poza katalogiem
`work` uploadera. Ten runbook kończy się raportem do Kocura. Nie idzie dalej.

## 0. Suma kontrolna, zanim cokolwiek podmienisz

W katalogu pakietu:

```bash
sha256sum -c SHA256SUMS
```

`SHA256SUMS` obejmuje wrapper, `nightly.sh`, przykład env, `VERSION` i ten
runbook. Plik `zulea-upbrr.env` z prawdziwymi ścieżkami jest lokalny i nie
wchodzi do sumy. Niezgoda sumy = STOP.

`nightly.sh` z pakietu nie wywołuje `dupe-accept`, `upload`, `resolve` ani
`seed`. Jeśli na hoście nightly ma już wpisane ścieżki, nie nadpisuj tych
zmiennych w ciemno. Porównaj tylko, że nie doszły komendy wysyłki ani akceptacji.

## 1. Obraz

Weź `MEDIAINFO_VERSION` z budowy obrazu `zulea-upbrr:5b87372` (ta sama wartość
co dziś; jest w jego `BUILD-INFO` / w argumencie, którym powstał). Nie zmieniaj
jej.

```bash
docker build -f scripts/zulea/docker/Dockerfile \
  --build-arg MEDIAINFO_VERSION=<ta sama co dziś> \
  -t zulea-upbrr:0.6.0 .
docker run --rm --entrypoint cat zulea-upbrr:0.6.0 /usr/local/share/zulea-upbrr/BUILD-INFO
docker run --rm --network none --entrypoint zulea-upbrr zulea-upbrr:0.6.0 version
```

Oczekiwane: `version` wypisuje `zulea-upbrr 0.6.0`. `BUILD-INFO` wpisz do
raportu. Stary obraz `zulea-upbrr:5b87372` zostaje.

Przed podmianą wrappera skopiuj montowania z działającego wrappera do
`zulea-upbrr.env` (wzorzec: `zulea-upbrr.env.example`). `ZULEA_IMAGE=zulea-upbrr:0.6.0`.

## 2. Wrapper i skrypty

Podmień wrapper plikiem `zulea-upbrr.sh` z tego pakietu po zgodności SHA.
Potem:

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
Biblioteka zostaje tylko do odczytu. Katalog `work` jest jedynym miejscem,
w którym uploader może pisać, i te dwie komendy nie wysyłają nic na tracker.

## 4. Raport i STOP

Raport do Kocura: `version` = 0.6.0, `BUILD-INFO`, SHA wrappera, `check-config`
= 0, wynik `verify-identity` i `check`.

**STOP.** W tym oknie nie uruchamiaj `dupe-accept`, `plan` dla uploadu,
`upload`, `resolve` ani `seed`. Akceptacja duplikatu i wysyłka są późniejszym
krokiem Andryou, po osobnej zgodzie, nie częścią tej podmiany.
