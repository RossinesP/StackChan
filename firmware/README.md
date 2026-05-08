
## Build

### Fetch Dependencies

```bash
python3 ./fetch_repos.py
```

### Tool Chains

[ESP-IDF v5.5.4](https://docs.espressif.com/projects/esp-idf/en/v5.5.4/esp32s3/index.html)
provides `idf.py` and the cross-compiler. One-time install (~3 GB):

```bash
mkdir -p ~/esp && cd ~/esp
git clone -b v5.5.4 --recursive https://github.com/espressif/esp-idf.git
cd esp-idf && ./install.sh esp32s3
```

In every shell where you build, source the export script first:

```bash
source ~/esp/esp-idf/export.sh
idf.py --version    # should print v5.5.4
```

First time in the project, set the chip target:

```bash
cd firmware
idf.py set-target esp32s3
```

### Build

```bash
idf.py build
```

### Flash

```bash
idf.py flash monitor    # `monitor` opens the serial log
```
