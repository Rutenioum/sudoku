# 数据转发实现与性能验证

数据转发保持既有配置、Classic/Packed 编码规则、AEAD 记录格式和 mux 帧格式兼容。优化集中在缓冲复用、减少复制和批量处理。

- Classic/Packed 按需创建读缓冲，直接借用 `bufio.Reader` 中已到达的字节解码；只负责写入的 codec 不分配读缓冲。握手回退需要的原始流录制仍然完整。
- Packed 按三字节块集中编码；无填充路径批量追加四个 hint。有填充路径保留随机数消费顺序、保护前缀和残余位标记。
- 最终编码缓冲按最多 32 KiB 的切片写出，避免大块写入导致的 TCP 吞吐下降。切片不增加复制；底层 Write 调用次数可能增加。
- AEAD 复用记录缓冲。读取目标足够大时直接解密到调用方缓冲，小读取保留待消费明文。认证失败不会推进接收状态。
- `RecordConn.WriteBuffers` 将 mux 帧头与载荷合入同一记录；跨切片数据在最终帧内聚合并原地加密。元数据包装层透传该能力，普通连接使用完整写入回退。
- mux 通过分级池管理接收块，`io.WriterTo` 直接将块交给下游。下游使用期间块不能归还池；短写后保留未写出的后缀。4 MiB 每流未读数据上限包含正在写出的块。远端关闭仍允许排空，本地关闭释放未消费数据。
- 双向转发优先使用 `io.WriterTo` / `io.ReaderFrom`；需要复制时借用数组指针缓冲池，避免切片装箱分配。

## 基准

完整 TCP 回环基准包括生产方向 codec、服务端元数据包装、AES-128-GCM 和 mux，首尾校验数据一致性。组件基准分别测量 AEAD 收发、mux 收发、编解码和连接构造。发送方领先量受限，避免测试自身触发接收队列上限。

```sh
go test ./internal/tunnel -run '^$' -bench '^BenchmarkLoopbackForward$' -benchmem -benchtime=1s -count=3
go test ./internal/tunnel ./pkg/crypto -run '^$' -bench 'Benchmark(MuxForward|RecordForward)' -benchmem -benchtime=500ms -count=3
go test ./pkg/obfs/sudoku -run '^$' -bench 'Benchmark(CodecConnConstruction|(Pure|Packed)(Read|Write)Throughput)' -benchmem -benchtime=500ms -count=3
```

版本对比应使用相同工具链、相同基准文件并顺序交替运行，不能把组件吞吐直接当作公网速度。回环基准固定填充比例以减少随机差异；默认填充组件基准和操作系统调度仍可能产生波动。`0 allocs/op` 表示摊销结果，冷启动、GC 清池、缓冲增长及密钥轮换仍可能分配。

## 回归验证

测试覆盖旧版编码样本、独立参考解码器、分片读取、批量写入、短写、缓冲生命周期、认证失败、密钥轮换、半关闭、并发关闭和接收队列上限。

```sh
go test ./... -count=1 -shuffle=on
go vet ./...
go test -race ./pkg/crypto ./pkg/connutil ./pkg/obfs/sudoku ./internal/tunnel
go test ./pkg/obfs/sudoku -run '^$' -fuzz '^FuzzPackedReadCompatibility$' -fuzztime=20s -parallel=2
```

Windows 全量测试可加 `-p 1`。Classic/Packed × off/stream/poll/ws × mux off/auto/on 的传输兼容性冒烟验证：

```powershell
$env:SUDOKU_DOWNLINK_CONCURRENT_CONNS = '4'
$env:SUDOKU_DOWNLINK_CONCURRENT_BYTES = '262144'
go test ./tests -run '^$' -bench BenchmarkDownlinkThroughputConcurrentMatrix -benchtime=1x -count=1 -timeout=180s
```
