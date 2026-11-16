# cryptopay-offline：稳定币充值离线密钥工具

在**离线（air-gapped）机器**上运行。生成助记词、导出服务器需要的只读材料（EVM xpub、签名的 Solana 地址池清单、签名公钥）。服务器永远拿不到助记词或任何私钥。

## 编译（在联网机器上编译，拷贝二进制到离线机）

本工具和联网端 `cryptopay-sweep` 在独立的 Go module `backend/tools/cryptopay` 里（它们依赖 go-ethereum / solana-go，
单独成 module 是为了让服务器程序的依赖完全不受影响）。

```bash
cd backend/tools/cryptopay
CGO_ENABLED=0 go build -trimpath -o cryptopay-offline ./cmd/cryptopay-offline
CGO_ENABLED=0 go build -trimpath -o cryptopay-sweep   ./cmd/cryptopay-sweep     # 联网端，不要拷到离线机
sha256sum cryptopay-offline   # 记录哈希，离线机上核对
```

各平台（linux/windows/darwin × amd64/arm64）的预编译离线二进制和 `SHA256SUMS` 由维护者单独提供；
拷到离线机后先 `sha256sum -c SHA256SUMS`（Windows 用 `certutil -hashfile 文件 SHA256` 逐个对照）。

## 离线机上的步骤

```bash
# 1. 生成 24 词助记词（crypto/rand，256 位熵）。写入新文件，权限 0600，已存在则拒绝覆盖。
./cryptopay-offline gen-mnemonic --out /secure/mnemonic.txt
#    立即抄到纸上/钢板上做备份。--print 会直接打到屏幕，仅在确实需要时用。

# 2. EVM（Arbitrum）：导出账户级 xpub（m/44'/60'/0'）和前 3 个地址
./cryptopay-offline evm-xpub --mnemonic-file /secure/mnemonic.txt
#    每个地址都会分别用“私钥路径”和“服务器同款 xpub 公钥派生代码”算一遍，不一致直接报错。

# 3. Solana：生成签名地址池清单（Phantom 兼容路径 m/44'/501'/{i}'/0'）
./cryptopay-offline solana-pool --mnemonic-file /secure/mnemonic.txt \
    --start 0 --count 5000 --out pool-0-4999.json
#    签名密钥 = m/44'/501'/K'/0'，K 默认 2147483000（远离地址池范围；在池范围内会被拒绝）。
#    写文件前会用服务器同款校验函数自检。追加地址池时用新的 --start（如 5000），同一个签名密钥。

# 4. 查看签名公钥（配置到服务器作为受信签名者）
./cryptopay-offline signer-pubkey --mnemonic-file /secure/mnemonic.txt

# 5. 抽查：在服务器后台看到某个用户地址后，回离线机核对
./cryptopay-offline verify-evm    --mnemonic-file /secure/mnemonic.txt --index 17
./cryptopay-offline verify-solana --mnemonic-file /secure/mnemonic.txt --index 17
```

助记词 BIP39 口令（passphrase）固定为空，与 MetaMask / Phantom 默认一致，导入钱包即可看到同样的地址。

## 交给服务器的东西（只读，可以离开离线机）

| 内容 | 来源 | 用途 |
|---|---|---|
| EVM xpub（`xpub6...`，深度 3、子序号 0'） | `evm-xpub` | 服务器派生 m/44'/60'/0'/0/i 充值地址；服务器会拒绝 xprv 和错误层级的 xpub |
| 前 3 个 EVM 地址 | `evm-xpub` | 导入后与服务器派生结果核对 |
| Solana 地址池清单 JSON | `solana-pool` | 服务器分配 Solana 充值地址（owner），并据此计算 USDC/USDT ATA |
| 签名公钥（base58） | `signer-pubkey` / `solana-pool` 输出 | 服务器配置为受信签名者；清单必须由它签名才能导入 |

清单的签名内容：`encoding/json` 序列化的清单结构体、`signature` 字段置空并省略（紧凑 JSON，字段顺序 version, network, path_template, start, entries, signer_pubkey），ed25519 签名后 base58 写入 `signature`。服务器解析文件后重新序列化再验签，所以文件的缩进格式不影响验签；未知字段会被拒绝。

## 绝不能离开离线机的东西

- 助记词文件和纸质备份。
- 任何 xprv / 私钥（本工具不输出私钥）。
- 不要把助记词拍照、粘贴到联网电脑、放进密码管理器的云同步。

把清单/xpub 拷出离线机时，用只读 U 盘或二维码；拷出前用 `cat` 看一遍文件里只有地址和公钥。


---

# 归集（sweep）与外发（send）

把各充值地址上的 USDC/USDT 先**归集到你自己的金库地址**，再从金库**外发**到币安等已登记的地址。
私钥只在离线机上出现；联网机只负责读链上数据（`prepare`）和广播（`broadcast`）。

## 角色和地址（全部由离线机从助记词派生，联网机和 plan 文件无法指定）

| 角色 | 路径 | 用途 |
|---|---|---|
| EVM 金库 | `m/44'/60'/1'/0/0` | Arbitrum：付 gas、给充值地址补 gas、接收归集、发起外发 |
| Solana 金库 | `m/44'/501'/2147483001'/0'` | Solana：付手续费、durable nonce 授权人、接收归集、发起外发 |
| Solana 清单签名 key | `m/44'/501'/2147483000'/0'` | 只签地址池清单，**永远不会**被当作归集来源 |
| 充值地址 | EVM `m/44'/60'/0'/0/i`；Solana `m/44'/501'/i'/0'`（i < 2147483000） | 归集的来源 |

## 安全保证（以及它们的边界）

信任模型：**联网机、RPC、plan.json 一律视为不可信**。离线机保证：

1. **归集的钱只会进金库**：离线机自己派生金库地址并自己编码每一笔交易（ERC-20 `transfer(金库, 金额)` / Solana
   `transferChecked(源 ATA → 金库 ATA)`），合约/mint/程序地址写死在代码里；每个来源都要从 index 重新派生并和 plan 的地址一致，
   否则整批拒签。离线机从不签 plan 里给的 calldata 或 message。签完后用独立解析器把签好的交易解码回来，逐字段和确认过的内容比对。
2. **外发只会去离线登记过的地址**：`send-sign` 只接受 `dest-register` 登记过的标签；登记时地址由你在离线机**手动输入两次**
   （EVM 大小写混合时校验 EIP-55）。金额由你在离线机命令行输入，不取自 plan。
3. **损失上限 = 你在离线机确认过的预算**：每次签名前显示金库最大支出 / 手续费上限，写死的上限不能被 plan 提高：
   - Arbitrum：maxFeePerGas ≤ 0.1 gwei，单笔 gasLimit ≤ 1,000,000，金库本操作最大支出 ≤ 0.002 ETH，每批 ≤ 50 个来源；
     加 `--allow-high-fee`（并再输入 `HIGH-FEE`）可放宽到绝对上限 1 gwei / 0.005 ETH。
   - Solana：每笔 ComputeUnitLimit 固定 200,000，优先费 ≤ 50,000 micro-lamports/CU，签名费 5000 lamports/签名，
     金库 ATA 创建租金 0.00203928 SOL（资金占用，不是燃烧）；每次操作（含租金）≤ 0.01 SOL，最多 5 笔交易。
   - 上限按**整条替换链累计**计算（见下面“账本”），反复重签不能绕过。
4. **不会双花**：离线账本保证每条链同一时刻只有一个未完成（pending）操作；Arbitrum 的新签名要么复用旧操作的全部 nonce
   （每个 nonce 链上只能被消费一次；新旧签名可能各成功一部分，但归集都进金库、一笔外发只占一个 nonce，不会外发两次；
   Solana 不允许替换），
   要么等你人工核对后关闭旧操作；用过的（签名地址, nonce）/（nonce 账户, nonce 值）永远不会再用于新签名。

**不保证的事**：
- plan 里的余额、nonce、手续费水平是否真实——撒谎的 plan 只能让交易失败或在预算内浪费手续费，不能把钱转走；
- 可用性：联网机被控制时可以不广播、乱广播（只能广播你签过的交易）；
- 你登记外部地址时看到的地址是否正确——这一步靠你**用手机交易所 App** 独立核对；
- Solana 手续费上限是按当前网络费率规则算出的上限，链上没有“总费用”字段强制；
- Solana nonce 账户的 authority 由你登记前在手机浏览器核对；离线机只保证交易里 advanceNonce 的授权人是金库，
  若链上 authority 不是金库，交易会被 System Program 拒绝（兜底，不是离线验证）；
- 离线账本本身：删除、回滚或手工编辑账本文件会破坏这些保证（账本里也有外部地址登记表；能改账本的人能改收款登记）。
  工具会重算账本的结构不变量（替换链、累计预算、nonce 唯一），不一致就拒签，但防不住整份恶意重写或用旧备份回滚。
  **账本和助记词一样要保护和备份，永远不要删除或重建**；
- 离线机上的确认输入（两次输入地址、最后 6 位、HIGH-FEE、close 的 op id）读自标准输入，不区分键盘还是管道：
  不要在离线机上运行任何来自联网机的脚本。

## 账本（离线机上，`--ledger` 文件，权限 0600）

- `ledger init` 只运行一次；文件不存在时签名命令会拒绝（防止敲错路径得到一个空账本）。
- 每次签名在**写出 signed 文件之前**先把操作记为 pending（临时文件 + fsync + rename + 目录 fsync，任何一步失败都不输出签名；
  **Windows 不能对目录 fsync，所以在 Windows 离线机上没有同等的断电保证**——建议离线机用 Linux/macOS；用 Windows 时，
  签名后先确认 `ledger list` 里有这条记录再拔 U 盘）。在 Linux/macOS 上，断电/崩溃最多留下“账本有记录、没有（或只有半截）输出文件”，
  这时用 `ledger export --ledger /secure/sweep-ledger.json --op <id> --out recovered-signed.json`（换一个新文件名）重新导出当时签好的交易（不签任何新东西、不增加预算）。
  账本损坏时拒绝签名。只有在**确定该备份之后没有签过任何东西**时才可以用备份恢复；否则先停下，
  在区块浏览器上独立核对每个签名地址的 nonce / 每个 nonce 账户的值，再决定怎么做。
- 同一条链有 pending 操作时，只允许：
  - **替换（仅 Arbitrum）**：新 plan 复用旧操作的全部 nonce（例如提高手续费重签）。屏幕会显示“本操作链累计最大授权”，上限按累计计算；
  - **Solana 永远不能替换**：离线机无法证明 plan 里的 nonce 值就是 nonce 账户里存的值（联网机可以填一个普通的近期 blockhash，
    这样两份不同的签名可能都上链）。所以 Solana 签名后只能：广播原签名；文件丢了用 `ledger export` 重新导出原签名；
    或者核对后关闭；
  - **人工关闭**：`ledger close --op <id>`。关闭前用手机打开它列出的区块浏览器链接核对，**broadcast 的结果文件只作参考，不能代替这一步**：
    - Arbitrum：每笔已成功或确定不会上链，并且每个签名地址当前的 nonce 已经大于列出的 nonce；
    - Solana（`ledger close` 会让你选 A 或 B）：
      A. 每笔都显示 **Finalized**（成功或失败都算）。**外发（send）只能用 A**；
      B. 仅限归集（sweep）：签名已超过 30 分钟（工具按离线机时钟检查），并且每个 nonce 账户当前（finalized）存的值
         都已经不是签名时的值。B 不能严格证明旧签名已失效（恶意 plan 可能用普通 blockhash，网络若停摆过它可能仍有效），
         所以只允许用于归集：旧归集签名即使之后上链，钱也只会进金库，手续费在已确认的预算内。
         B 不撤销旧签名，也不释放它的费用授权：总费用上限是你确认过的各次预算之和，而不是新操作单独显示的预算。
      只看“浏览器找不到交易”永远不够：真正的 durable nonce 签名可以被扣住很久再发。
      外发签名迟迟不能 finalized（例如联网机被控制、给了假的 nonce 值）时，不要关闭，先停下来找维护者人工核对。
  - 若某笔旧签名还没上链：不要关闭，直接把原来的 signed 文件再 broadcast 一次（签名一直有效）；Arbitrum 也可以重新 prepare 同一批来源做替换。

## 第一次归集：一步一步

下面 `离线$` 表示在离线机上运行，`联网$` 表示在联网电脑上运行。文件在两台机器之间用 U 盘搬运。

### 0. 准备（只做一次）

```bash
离线$ ./cryptopay-offline treasury-address --mnemonic-file /secure/mnemonic.txt
#   记下两行地址：EVM 金库（0x…）和 Solana 金库。用手机拍照/抄写带到联网机。
离线$ ./cryptopay-offline ledger init --ledger /secure/sweep-ledger.json
```

### 1. 给 EVM 金库充一点 ETH（Arbitrum One）

在币安 App：提现 → ETH → 网络选 **Arbitrum One** → 地址填 EVM 金库 → 0.001 ETH 足够很多次归集
（每个来源一次大约 0.000005 ETH 级别）。到账后用手机在 arbiscan.io 搜金库地址确认余额。

### 2. Solana：建 durable nonce 账户（只做一次），顺便给 Solana 金库充 SOL

Solana 普通交易 60–90 秒过期，无法跨离线机，所以要用 durable nonce 账户。联网机会生成一个**随机临时地址**
（不来自助记词，私钥写在 `--key-out` 文件里），你往它充 SOL，它创建 nonce 账户（授权人 = 你的 Solana 金库），
**剩下的 SOL 自动转进 Solana 金库**——所以只需从币安提一次。

```bash
联网$ ./cryptopay-sweep nonce-setup --helius-key-file ~/.helius_key \
        --treasury <Solana金库地址> --key-out nonce-tmp-key.json
#   屏幕显示临时地址和最低金额。在币安 App 提 SOL（网络 Solana）到这个临时地址，建议 0.03 SOL
#   （≈ nonce 账户租金 0.0015 + 金库 ATA 租金 0.002 + 金库保留 0.0009 + 很多次手续费）。
#   程序等到账后自动完成；中断了用 --resume nonce-tmp-key.json 继续（已建好的 nonce 账户会跳过，只等还缺的金额，
#   最后把余额转进金库）。临时 key 文件里有少量 SOL 的私钥，流程结束、余额转走后可以删除。
```

用手机打开它打印的 `https://solscan.io/account/<nonce账户>`，确认是 Nonce Account 且 **Authority = 你的 Solana 金库**，然后：

```bash
离线$ ./cryptopay-offline nonce-register --ledger /secure/sweep-ledger.json --address <nonce账户>
```

最多登记 5 个 nonce 账户；一个 nonce 账户每轮装一笔交易（一笔约 5 个来源），所以一轮 Solana 最多约 25 个代币账户，
更多的下一轮再归集（`prepare` 会提示剩余数量）。

### 3. 列出要归集的来源（index + 地址）

只读查询服务器数据库（或请维护者导出），每行 `index 地址`：

```sql
SELECT derivation_index, address FROM crypto_pay_addresses WHERE network = 'arbitrum-one'   ORDER BY 1;
SELECT derivation_index, address FROM crypto_pay_addresses WHERE network = 'solana-mainnet' ORDER BY 1;
```

如果导入过多套 keyset（换过助记词），再加 `AND keyset_id = <当前 keyset>` 只取本助记词的地址。
存成 `arb-sources.txt` / `sol-sources.txt`。index 写错不会出事：离线机重新派生对不上会整批拒签。

### 4. Arbitrum 归集

```bash
联网$ ./cryptopay-sweep prepare --network arbitrum-one --treasury <EVM金库> \
        --sources-file arb-sources.txt --out arb-plan.json
#   没有余额的来源自动跳过；金库 ETH 不够会报错并告诉你差多少。RPC 默认 Arbitrum 公共节点，
#   也可 --rpc-file 文件（文件里放 URL，避免 key 进命令历史）。
#   把 arb-plan.json 拷到离线机
离线$ ./cryptopay-offline sweep-sign --mnemonic-file /secure/mnemonic.txt --ledger /secure/sweep-ledger.json \
        --plan arb-plan.json --out arb-signed.json
#   逐项核对屏幕：收款方 = 你的 EVM 金库、合计金额、金库最大支出；输入金库地址最后 6 位确认。
#   把 arb-signed.json 拷回联网机
联网$ ./cryptopay-sweep broadcast --signed arb-signed.json
#   顺序：先补 gas（收据成功）→ 再转代币（核对 Transfer 事件）；任何异常立即停止，不会自己重试或补救。
#   每次运行对每笔交易最多发送一次；再次运行可能把同一份已签名交易重新发送一次（同一个签名，链上最多执行一次，无害），
#   它永远不会签新交易或发送签名文件以外的东西。
#   结果写在 arb-signed.json.result.json；默认等 Arbitrum finalized（约 15–30 分钟），再跑一次可续查。
```

用手机打开每笔 `https://arbiscan.io/tx/<hash>` 确认 Success、代币到了金库；然后：

```bash
离线$ ./cryptopay-offline ledger close --ledger /secure/sweep-ledger.json --op <op id>
```

### 5. Solana 归集

```bash
联网$ ./cryptopay-sweep prepare --network solana-mainnet --helius-key-file ~/.helius_key \
        --treasury <Solana金库> --sources-file sol-sources.txt --nonce-account <nonce账户> --out sol-plan.json
离线$ ./cryptopay-offline sweep-sign --mnemonic-file /secure/mnemonic.txt --ledger /secure/sweep-ledger.json \
        --plan sol-plan.json --out sol-signed.json
联网$ ./cryptopay-sweep broadcast --helius-key-file ~/.helius_key --signed sol-signed.json
```

手机打开 `https://solscan.io/tx/<签名>` 确认成功、并确认 nonce 账户的值已变化，然后在离线机 `ledger close`。

### 6. 外发到币安

先登记一次币安的充值地址（每条链各一个标签）：在**手机币安 App** → 充值 → USDT（或 USDC）→ 选网络
（Arbitrum One / Solana）→ 看到地址，在离线机照着输入两次：

```bash
离线$ ./cryptopay-offline dest-register --ledger /secure/sweep-ledger.json --network arbitrum-one   --label binance-arb
离线$ ./cryptopay-offline dest-register --ledger /secure/sweep-ledger.json --network solana-mainnet --label binance-sol
```

注意：币安 Solana 充值地址是钱包地址（owner）。工具会拒绝明显不是钱包的地址（ATA 等 PDA 地址），
但无法证明一个地址一定是钱包，所以请确认你抄的是 App 上“充值地址”而不是别的。Solana 外发要求该地址上这个币的
token 账户已存在（`prepare` 会检查；不存在说明地址或网络选错了），离线机不会替它创建。

```bash
联网$ ./cryptopay-sweep prepare --kind send --network arbitrum-one --treasury <EVM金库> \
        --asset USDT --dest <币安地址，仅用于估算和核对> --out send-plan.json
离线$ ./cryptopay-offline send-sign --mnemonic-file /secure/mnemonic.txt --ledger /secure/sweep-ledger.json \
        --plan send-plan.json --dest binance-arb --amount 2.7 --out send-signed.json
#   屏幕显示收款方 = 登记的 binance-arb 地址；输入它的最后 6 位确认。plan 里的 dest 与登记的不一致会直接拒签。
联网$ ./cryptopay-sweep broadcast --signed send-signed.json
```

Solana 外发：

```bash
联网$ ./cryptopay-sweep prepare --kind send --network solana-mainnet --helius-key-file ~/.helius_key \
        --treasury <Solana金库> --asset USDT --dest <币安Solana地址，仅用于检查> --nonce-account <nonce账户> --out sol-send-plan.json
离线$ ./cryptopay-offline send-sign --mnemonic-file /secure/mnemonic.txt --ledger /secure/sweep-ledger.json \
        --plan sol-send-plan.json --dest binance-sol --amount 4.3 --out sol-send-signed.json
联网$ ./cryptopay-sweep broadcast --helius-key-file ~/.helius_key --signed sol-send-signed.json
```

到账后在币安 App 确认，再在离线机 `ledger close`。上面的 `离线$`/`联网$` 是提示符，`<…>` 要换成实际值。

## 出问题时

- `broadcast` 停下（STOPPED）：不要手工补发别的交易。看提示用手机核对链上状态；
  - 还没上链、nonce 没被占用：再跑一次 `broadcast`。它先查链上状态；若仍未上链，会把同一份已签名交易再发送一次
    （同一个签名，最多执行一次）。每次运行每笔最多发送一次，不会在后台循环重发，也不会签新交易；
  - nonce 已被别的交易占用 / 交易失败 / Solana nonce 已前进：核对后 `ledger close`，再从 `prepare` 重新来。
- 手续费太低一直不上链（Arbitrum 很少见）：重新 `prepare` 同一批来源（nonce 不变），离线机重签即为替换（累计预算）。
  Solana 不能替换：继续用原 signed 文件 broadcast，或按上面的条件核对后 `ledger close` 再重新 `prepare`。
- `sweep-sign` 报 “still pending”：上一个操作没关闭，先按上面核对后 `ledger close`。
- `sweep-sign` 报 “already used”：plan 用了已经签过的 nonce（旧 plan）；重新 `prepare`。
- 账本锁文件残留（`<账本>.lock`）：确认没有别的 cryptopay-offline 在运行，先 `ledger list` 看看，再删除锁文件。
- 广播锁残留（联网机用户缓存目录 `cryptopay-sweep/locks/<网络>-<地址>.lock`）：确认没有别的 broadcast 在运行后删除。
- signed 文件丢了或写坏了：离线机 `./cryptopay-offline ledger export --ledger /secure/sweep-ledger.json --op <id> --out recovered-signed.json`。
