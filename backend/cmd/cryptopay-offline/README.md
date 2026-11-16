# cryptopay-offline：稳定币充值离线密钥工具

在**离线（air-gapped）机器**上运行。生成助记词、导出服务器需要的只读材料（EVM xpub、签名的 Solana 地址池清单、签名公钥）。服务器永远拿不到助记词或任何私钥。

## 编译（在联网机器上编译，拷贝二进制到离线机）

```bash
cd backend
CGO_ENABLED=0 go build -trimpath -o cryptopay-offline ./cmd/cryptopay-offline
sha256sum cryptopay-offline   # 记录哈希，离线机上核对
```

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

## future: sweep（归集，未实现）

归集（把各充值地址的 USDC/USDT 转到冷钱包）不在本 PR 范围，后续单独 PR（计划中的 ④ Ops 离线归集工具）：

- 联网机器生成未签名交易（nonce、gas、最新 blockhash），离线机签名，联网机器广播。
- EVM：USDC 支持 permit，但 USDT0 需先在固定区块主网 fork 上实测 permit+transferFrom；未验证前保留“先补 gas 再转”路径。
- Solana：owner 签名转出 ATA 余额；租金/手续费由归集付款账户承担。
- 归集只能是人工批准的离线流程，服务器端不会出现任何签名能力。
