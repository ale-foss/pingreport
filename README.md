# PingReport End-User README

## What is PingReport?
PingReport turns a network test file (PingResult.txt) into a visual report you can view in your web browser. No installation or command line needed.

---

## Pre-requisites
- **Windows PC**: Windows 7 or later (Windows 10/11 recommended)
- **Web browser**: Chrome, Firefox, Edge, or any modern browser with JavaScript enabled
- **USB stick**: FAT32 format, 16GB or less
- **Ping result file**: PingResult.txt (created automatically by the phone during testing)

---

## How to use
1. Double-click `pingreport.exe` (or run it from the command line).
2. A folder selection dialog opens — pick the folder containing your `PingResult_*.txt` files.
3. The report (HTML file) is created next to the selected folder and opens automatically in your default web browser.

> If the browser does not open automatically, open the generated `.html` file manually.

**Command line:**
```
pingreport C:\path\to\folder
pingreport -dir C:\path\to\folder --html my_report.html --csv data.csv
```

---

## Important
- Make sure JavaScript is enabled in your browser for the report to display correctly.
- The ping result file is always named PingResult.txt (you can rename it if needed).

---

## License
Copyright © 2026 ALE International
MIT License
Free to use, copy, and distribute. See [LICENSE](LICENSE) for full text.

This software bundles the following third-party components:

| Component | Version | License | Copyright |
|---|---|---|---|
| [Plotly.js](https://github.com/plotly/plotly.js) | 1.58.5 | MIT | 2012-2021 Plotly, Inc. |
| [sqweek/dialog](https://github.com/sqweek/dialog) | — | ISC | 2018 the dialog authors |
| [TheTitanrain/w32](https://github.com/TheTitanrain/w32) | — | BSD 3-Clause | 2010-2012 The w32 Authors |

---

*Refer to the project documentation for more information.*
