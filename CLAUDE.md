# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

go-deadman 是以 Bubble Tea 製作的 TUI，持續對一組目標發送 ICMP ping（pro-bing），顯示每個目標的丟包率、RTT、移動平均、jitter 與 sparkline 歷史。程式註解與錯誤訊息以繁體中文為主；README 與畫面文字為英文。

完整的建置／測試指令、各套件職責與慣例都寫在 [.github/copilot-instructions.md](.github/copilot-instructions.md)，修改前先讀那份。以下只列出容易踩到的重點。

## 指令

```sh
go build -trimpath -ldflags="-s -w" -o go-deadman ./cmd/go-deadman   # 內嵌所有 presets/*.yaml
go vet ./... && GOOS=linux go vet ./... && GOOS=darwin go vet ./...
go test -count=1 ./...
go test ./internal/monitor -run '^TestName$' -count=1
```

- 使用者本機可能沒有安裝 Go，平常靠 GitHub Actions 編譯與驗證。若需要在本機驗證，就在 scratchpad 放一份免安裝的 Go 來跑，不要裝到系統上。
- Windows 本機沒有 cgo，`-race` 只在 CI 上跑。
- CI 會執行 `go mod tidy -diff` 與 `gofmt -l .`，go.mod 沒 tidy 或沒跑 gofmt 都會失敗。
- CI 的 action 都鎖 commit SHA、工具都鎖版本；更新時一併更新註解中的版本號。gosec 目前鎖在 master 的 commit，因為正式版 v2.29.0 不支援 Go 1.27。

## 架構重點

- 結構：`cmd/go-deadman`（進入點）、`internal/monitor`（設定、worker、統計）、`internal/tui`（畫面）、`presets`（內嵌所有 YAML，執行時以 `-preset` 選擇；執行檔名為 `go-deadman-<preset>` 時自動選該 preset）。
- Windows 的 IPv4 native 走 `icmp_windows.go` 的 `IcmpSendEcho`（不需系統管理員），RTT 用 `QueryPerformanceCounter` 計時，因為 Windows 上 `time.Now` 的解析度不足以量 loopback／區網。這段只有在 Windows 上才會編譯與測試（本機與 CI 的 windows-latest）。
- `LoadConfig` 負責驗證與標記分隔列。`isSep` 在 worker 啟動前設定、之後唯讀，所以 `IsSeparator()` 不需加鎖；其他 Device 欄位一律經 `mu` 存取，畫面透過 `Snapshot()` 讀取。
- 兩條 ping 路徑的結果都只能經 `Device.UpdateStats` 寫入。native 路徑以 `probeTracker` 讓每個序號只結算一次；`OnSend` 與 `OnRecv` 共用同一個 channel，以保證處理順序。
- worker 的生命週期由 `main` 控制：`cfg.Start(ctx)` 啟動，結束時 cancel 並等待（最多 2 秒）。新增任何會等待或阻塞的程式碼，都要能被 ctx 中斷（用 `sleepCtx`、`CommandContext`、`DefaultResolver` 的 ctx 版本）。
- 測試替換點：`newEchoSession`、`runPing`、`lookupIP`、`lookupAddr`、`dnsRetryMin/Max`。替換後必須在 cleanup 時先停掉 worker 再還原（`worker_test.go` 的 `goWorker`），否則 CI 的 `-race` 會抓到。`internal/monitor` 覆蓋率要維持 ≥ 80%（CI 有門檻）。
- 重繪是按需觸發：`signalUpdate()` → `monitor.Updates()` → tui 的 `dataMsg`，再以 `renderFPS` 節流。新增會改變畫面的狀態時，記得呼叫 `signalUpdate()`。
- 列數超過 `height - chromeRows` 時以 `offset` 捲動；欄寬由 `computeLayout` 依優先順序分配；文字寬度一律用 `lipgloss.Width`／`truncWidth`／`fitWidth` 計算（CJK 佔 2 格）。

## 指標定義

- 非零丟包不會顯示成 `0%`，未滿 100% 也不會進位成 `100%`。
- 最近一次探測失敗時，RTT 欄顯示 `FAIL`。
- jitter 只配對相鄰兩次都成功的探測。
- SNT 是已結算（回覆或逾時）的探測數。
- 探測逾時為 `max(interval, 1s)`，native 與 fallback 共用。

修改指標時要同步更新 README 的「Columns」一節與測試。

## 發布

推送 `v*` tag 即會觸發建置（每個平台一個 zip）、attestation 與 Release；版本號會由 tag 注入 `main.version` 與 winres。Release 是 zip 包，裡面的執行檔固定命名為 `go-deadman.exe`，與 winres 的 `OriginalFilename` 一致，請勿改成不一致的名稱。`cmd/go-deadman/main.go` 的 `version` 預設值請一併更新。

`docs/screenshot.png` 是用真實資料產生的：暫時加一個測試去啟動 internet preset、等 90 秒後輸出 `View()` 的 ANSI 文字，再用 `charmbracelet/freeze --language ansi` 轉成 PNG。轉圖時要給足夠的 `--width`，因為方塊字元在 freeze 的預設字型下較寬。
