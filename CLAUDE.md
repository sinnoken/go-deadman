# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

go-deadman 是以 Bubble Tea 製作的 TUI，持續對一組目標發送 ICMP ping（pro-bing），顯示每個目標的丟包率、RTT、移動平均、jitter 與 sparkline 歷史。程式註解與錯誤訊息以繁體中文為主；README 為英文。

完整的建置／測試指令、各檔職責與慣例都寫在 [.github/copilot-instructions.md](.github/copilot-instructions.md)，修改前先讀那份。以下只列出容易踩到的重點。

## 指令

```sh
go build -trimpath -ldflags="-s -w" -o go-deadman .              # 內嵌 presets/internet.yaml
go build -trimpath -ldflags="-s -w" -tags tasks -o go-deadman .  # 內嵌 presets/tasks.yaml
go vet ./... && go vet -tags tasks ./...
go test -count=1 ./... && go test -count=1 -tags tasks ./...
go test . -run '^TestName$' -count=1
```

- 使用者本機可能沒有安裝 Go，平常靠 GitHub Actions 編譯與驗證。若需要在本機驗證，就在 scratchpad 放一份免安裝的 Go 來跑，不要裝到系統上。
- Windows 本機沒有 cgo，`-race` 只在 CI 上跑。
- CI 會執行 `go mod tidy -diff` 與 `gofmt -l .`，go.mod 沒 tidy 或沒跑 gofmt 都會失敗。

## 架構重點

- preset 用 build tag 選擇（`preset_internet.go` 為 `!tasks`、`preset_tasks.go` 為 `tasks`），不再複製出 `config.yaml`。`-config` 參數可在執行期改讀外部檔。
- `loadConfig`（config.go）負責驗證與標記分隔列。`Device.isSep` 在 worker 啟動前設定、之後唯讀，`Init`／`View` 才能不加鎖讀它；其他 Device 欄位一律經 `mu` 存取，`View` 透過 `snapshot()` 讀取。
- 兩條 ping 路徑的結果都只能經 `Device.UpdateStats`（stats.go）寫入。native 路徑以 `probeTracker`（ping.go）讓每個序號只結算一次；`OnSend` 與 `OnRecv` 共用同一個 channel，以保證處理順序。
- 重繪是按需觸發：`signalUpdate()` → `updateCh` → `dataMsg`，再以 `RENDER_FPS` 節流（ui.go）。新增會改變畫面的狀態時，記得呼叫 `signalUpdate()`。
- 欄寬由 `computeLayout` 依優先順序分配；文字寬度一律用 `lipgloss.Width`／`truncWidth`／`fitWidth` 計算（CJK 佔 2 格）。`TestViewFitsWidth` 會確認寬度 20～200 時，每一行都不超出終端寬度。

## 指標定義（來自 PLAN.md，已實作）

- 非零丟包不會顯示成 `0%`，未滿 100% 也不會進位成 `100%`。
- 最近一次探測失敗時，RTT 欄顯示 `FAIL`。
- jitter 只配對相鄰兩次都成功的探測。
- SNT 是已結算（回覆或逾時）的探測數。
- 探測逾時為 `max(interval, 1s)`，native 與 fallback 共用。

修改指標時要同步更新 README 的「Dashboard」一節與測試。

## 發布

推送 `v*` tag 即會觸發建置與 Release；版本號會由 tag 注入 `main.version` 與 winres。`main.go` 裡的 `version` 預設值與 README 徽章請一併更新。
