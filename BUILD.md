# Build & Publish — hướng dẫn nội bộ

Tài liệu này mô tả cách đóng gói `figma-mcp-console` lên npm. Gói npm **tự chứa** cả Go
binary lẫn Figma plugin — **không phụ thuộc GitHub** khi user cài đặt.

## Cấu trúc

```
plugin/                    ← NGUỒN plugin (sửa ở đây), git-tracked
 ├── code.js
 ├── ui.html
 └── manifest.json

npm/
 ├── package.json          ← git-tracked
 ├── run.js                ← launcher (chọn binary theo máy + lệnh install-plugin), git-tracked
 ├── build.sh              ← script build, git-tracked
 └── bin/                  ← TỰ SINH bởi build.sh, gitignored (KHÔNG commit)
      ├── darwin-arm64/figma-mcp
      ├── darwin-amd64/figma-mcp
      ├── linux-arm64/figma-mcp
      ├── linux-amd64/figma-mcp
      ├── windows-amd64/figma-mcp.exe
      ├── windows-arm64/figma-mcp.exe
      └── plugin/{code.js, ui.html, manifest.json}   ← BẢN COPY từ plugin/
```

> **Quy tắc vàng:** chỉ sửa plugin ở `plugin/` (nguồn). `npm/bin/plugin/` là bản copy, sẽ bị
> `build.sh` ghi đè mỗi lần build.

## Lệnh build (1 lệnh làm hết)

```bash
bash npm/build.sh
```

Script này làm 2 việc:
1. **Build 6 binary** cho tất cả OS/arch (cross-compile ngay trên máy bạn, ~10–45s):
   ```bash
   CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> \
     go build -trimpath -ldflags="-s -w" \
     -o npm/bin/<os>-<arch>/figma-mcp[.exe] ./cmd/figma-mcp
   ```
   - `CGO_ENABLED=0`: Go thuần → cross-compile mọi target, không cần C compiler/Docker.
   - `-trimpath`: xoá đường dẫn máy bạn khỏi binary.
   - `-ldflags="-s -w"`: bỏ debug symbol → binary nhẹ ~40%.
2. **Copy plugin** `plugin/*` → `npm/bin/plugin/`:
   ```bash
   cp plugin/code.js plugin/ui.html plugin/manifest.json npm/bin/plugin/
   ```

## Quy trình MỖI LẦN release (3 bước)

```bash
# 1. Sửa "version" trong npm/package.json  (bắt buộc — npm cấm publish trùng version)
# 2. Build lại (nếu sửa Go hoặc plugin thì BẮT BUỘC):
bash npm/build.sh
# 3. Publish:
cd npm && npm publish
```

Khi nào cần build lại:

| Sửa gì | Cần `build.sh`? | Vì sao |
|---|---|---|
| Go (`cmd/`, `internal/`) | ✅ Có | binary được biên dịch |
| Plugin (`plugin/*`) | ✅ Có | phải copy vào `npm/bin/plugin/` |
| `run.js` / `package.json` / `README` | ❌ Không (vẫn bump version + publish) | JS/text thuần |

> An toàn nhất: **luôn chạy `build.sh` trước mỗi `npm publish`**. npm KHÔNG tự biên dịch —
> nếu quên build, gói sẽ chứa binary/plugin CŨ.

## Kiểm tra trước khi publish

```bash
bash npm/build.sh
cd npm && npm pack                       # tạo .tgz thử
tar tzf figma-mcp-console-*.tgz          # phải thấy run.js + bin/** + bin/plugin/**
rm -f figma-mcp-console-*.tgz            # dọn file thử

# Test chạy local (không mạng):
node npm/run.js install-plugin           # phải in path manifest.json
printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}\n' \
  | node npm/run.js                      # phải thấy JSON "serverInfo"
```

## Build binary test local (tùy chọn)

Nếu chỉ muốn 1 binary để chạy thử trên máy mình (không phải publish):

```bash
go build -o bin/figma-mcp ./cmd/figma-mcp
```

(`bin/` ở repo root đã gitignore — chỉ là artifact dev, không ảnh hưởng npm.)
