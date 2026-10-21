# go-deadman 程式碼檢視報告

- **專案**：[sinnoken/go-deadman](https://github.com/sinnoken/go-deadman)
- **描述**：以 Go + Bubble Tea + pro-bing 實作的網路延遲 / 抖動 / 掉包監控 TUI
- **檢視範圍**：`main.go`（626 行，單檔）、`internet.yaml`、`tasks.yaml`、`PLAN.md`、`.github/`、CI 設定
- **檢視日期**：2026-10-21
- **檢視者**：Claude (Opus 4.8)

---

## 總評

以「個人用的區網 / 網際網路延遲監控 TUI」來說，品質**中上、可正常運作，網路層的設計比一般 AI 產出細緻**。但**不是生產級**：沒有測試、開箱不可重現、設定在編譯時寫死，還有幾個可疑的實作選擇。

另外要點明：這個專案是「小規模、有授權目標」路線的實作，**架構上無法延伸到全網高速掃描**（詳見最後一節）。

| 面向 | 評價 |
|---|---|
| 功能正確性 | 大致正確，但 timer / 掉包計數邏輯偏脆 |
| 網路層設計 | 佳（native→fallback 降級、socket 重用） |
| 可維護性 | 普通（單檔、AI 註解殘留、無測試） |
| 可重現建置 | 差（go.mod 未進版控、config 需手動產生） |
| 擴充性 | 僅適用數百個目標以內 |

---

## 做得好的地方

1. **native → fallback 優雅降級**
   先試 pro-bing 的免權限 ICMP（`SetPrivileged(false)`），失敗自動轉呼叫系統 `ping` 指令（`fallbackWorker`）。考慮到無權限環境，是全專案最值得稱讚處。

2. **Socket 重用**
   `pinger.Count = -1` 無窮發送模式，不是每次 tick 重開 pinger，效率正確。

3. **O(1) 滑動平均**
   以 `WindowSum` 增量維護滑動視窗（`main.go:154-161`），避免每次重算整個視窗。

4. **掉包計數用序號缺口回補**
   收到 `seq=10` 但預期為 5 時，補記 5~9 為掉包（`main.go:294-297`）；並以 `pkt.Seq < expectedSeq` 丟棄晚到封包避免重複計數（`main.go:289`）。

5. **併發與工程衛生**
   RWMutex 分離資料更新與畫面渲染、DNS 雙向解析、CI 具 golangci-lint / govulncheck / gosec，並啟用 dependabot。

---

## 問題清單

### 設計層面

| # | 嚴重度 | 問題 | 位置 |
|---|---|---|---|
| D1 | 高 | `config.yaml` 於**編譯時 embed**，改監控目標需重新編譯；且該檔未進 repo，需自行 `cp internet.yaml config.yaml` | `main.go:29-30` |
| D2 | 中 | `go.mod` / `go.sum` 未進版控，需手動 `go mod init` 才能建置，開箱不可重現 | repo 根目錄 |
| D3 | 中 | **完全沒有測試**。`PLAN.md` 詳列要補回歸測試，但一個測試檔都沒有——屬「規劃」非「完成」 | 全專案 |

### 實作層面

| # | 嚴重度 | 問題 | 位置 |
|---|---|---|---|
| I1 | 中 | `runtime.LockOSThread()` 把每個 worker 永久綁定一條 OS thread（迴圈永不返回，defer Unlock 實際不會執行）。pro-bing 不需要此機制，目標一多即浪費執行緒 | `main.go:231` |
| I2 | 中 | fallback timeout 不一致：Linux 寫死 `-W 1`，Windows 用 `interval` 算的 `timeoutMs`；interval < 1s 時 ctx 會比 `-W` 先終止 ping | `main.go:357, 360` |
| I3 | 中 | fallback 每 interval、每台都 fork 一個 `ping` 行程，且依賴語系字串解析（`時間=`），目標一多即不可擴充 | `main.go:328-385` |
| I4 | 低 | 固定 20 FPS 重繪，不論畫面是否變化，閒置仍持續重建整個 View 並產生 fmt 配置，空燒 CPU | `main.go:40, 419` |
| I5 | 低 | timer 邏輯偏脆：timeout 計時器與 seq-gap 是兩套獨立掉包時鐘，靠丟棄晚到封包對帳；recv 時 reset `timeoutDuration`、timeout 後 reset `interval`，細節易改壞 | `main.go:294, 312, 314, 319` |

### 程式碼氣味

| # | 問題 | 位置 |
|---|---|---|
| S1 | AI 迭代註解殘留：「✅ 建議一/建議三」「⭐」emoji、const 旁寫版本升級說明 | `main.go:37, 144, 154, 567` |
| S2 | 縮排不一致，該區塊從 tab 變成空格 | `main.go:556-566` |
| S3 | `rowFormatStr` 先 `fmt.Sprintf` 組格式字串再 `fmt.Sprintf` 一次，繞 | `main.go:571-572` |
| S4 | `truncate` 當 `maxLen < 2` 直接回傳原字串、不截斷，邊界行為怪 | `main.go:105` |

---

## 與「全網高速掃描」目標的對照

這個工具是「小規模、有授權目標」路線的實作：ping 一份**固定精選清單**（ISP、IX、DNS、台灣節點、區網 192.168.77.x），採「**每個目標一條 goroutine + 一個 socket + 一條 OS thread**」。

- 該架構約到**數百至一兩千個目標**即會被 goroutine / OS thread / 逐行 exec 拖垮。
- 離全網掃描所需的 ~74 萬 pps 差數個數量級。
- 若要走全網，**此程式碼不能當基礎**，須改用無狀態掃描器架構（送收分離、自組封包、不每目標開 socket）。

**結論**：當「盯一批重要節點的延遲 / 抖動 / 掉包」儀表板用，稱職；當全網掃描起點用，不適合。

---

## 建議的後續動作

### 方案 A — 把它改好（維持監控盤定位）
1. 改為 **runtime 讀取 config**，拿掉編譯時 embed（解 D1）。
2. 提交 `go.mod` / `go.sum`（解 D2）。
3. **移除 `runtime.LockOSThread()`**（解 I1）。
4. 統一 fallback timeout 與 interval 的關係（解 I2）。
5. 改為**按需重繪**（資料有更新才送 tick），取代固定 20 FPS（解 I4）。
6. 補上 `PLAN.md` 已規劃的回歸測試：掉包率格式、統計、ping 輸出解析、設定驗證、窄終端渲染（解 D3）。
7. 清理 AI 殘留註解與縮排（解 S1–S4）。

### 方案 B — 另起全網掃描骨架
若目標是全網 / 高 pps，不要在此專案上疊加，改以無狀態架構重寫：送收分離、封包自組、識別資訊編入封包、timeout 僅於結尾固定等候一次。

---

*本報告為程式碼靜態檢視結論；未實際編譯或執行該專案。行號對應檢視當日 repo 的 `main.go`。*
