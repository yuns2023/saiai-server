"""Independent cross-check vectors for sweep (see vectors_test.go).

Builds and signs the same transactions as the Go tests with independent
implementations: eth-account (Python, its own BIP32/RLP/EIP-1559 signing) and
solders (Rust solana-sdk message compiler + ed25519-dalek). Output:
testdata/xcheck_expected.json. Regenerate with:

  uv venv .venv && VIRTUAL_ENV=$PWD/.venv uv pip install solders==0.29.0 eth-account==0.14.0
  .venv/bin/python -I sweep/testdata/xcheck.py > sweep/testdata/xcheck_expected.json
"""
import hashlib, json, struct

from eth_account import Account
from solders.hash import Hash
from solders.instruction import AccountMeta, Instruction
from solders.keypair import Keypair
from solders.message import Message
from solders.pubkey import Pubkey
from solders.transaction import Transaction

MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
USDT_ARB = "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9"
USDT_SOL = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
TOKEN_PROGRAM = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
ATA_PROGRAM = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
SYSTEM = "11111111111111111111111111111111"
COMPUTE = "ComputeBudget111111111111111111111111111111"
RECENT_BH = "SysvarRecentB1ockHashes11111111111111111111"

Account.enable_unaudited_hdwallet_features()


def fake_key(label):
    return Pubkey.from_bytes(hashlib.sha256(label.encode()).digest())


def evm():
    treasury = Account.from_mnemonic(MNEMONIC, account_path="m/44'/60'/1'/0/0")
    src0 = Account.from_mnemonic(MNEMONIC, account_path="m/44'/60'/0'/0/0")
    data = bytes.fromhex("a9059cbb") + bytes(12) + bytes.fromhex(treasury.address[2:]) + (2700000).to_bytes(32, "big")
    b = src0.sign_transaction({"type": 2, "chainId": 42161, "nonce": 0, "maxPriorityFeePerGas": 0,
                               "maxFeePerGas": 20000000, "gas": 150000, "to": USDT_ARB, "value": 0, "data": data})
    a = treasury.sign_transaction({"type": 2, "chainId": 42161, "nonce": 7, "maxPriorityFeePerGas": 0,
                                   "maxFeePerGas": 20000000, "gas": 60000, "to": src0.address,
                                   "value": 150000 * 20000000, "data": b""})
    return {"treasury": treasury.address.lower(), "source0": src0.address.lower(),
            "topup_raw": "0x" + a.raw_transaction.hex(), "sweep_raw": "0x" + b.raw_transaction.hex()}


def sol_kp(seed, i):
    return Keypair.from_seed_and_derivation_path(seed, f"m/44'/501'/{i}'/0'")


def ata(owner, mint):
    return Pubkey.find_program_address([bytes(owner), bytes(Pubkey.from_string(TOKEN_PROGRAM)), bytes(Pubkey.from_string(mint))],
                                       Pubkey.from_string(ATA_PROGRAM))[0]


def sol():
    seed = hashlib.pbkdf2_hmac("sha512", MNEMONIC.encode(), b"mnemonic", 2048)
    treasury = sol_kp(seed, 2147483001)
    dest = sol_kp(seed, 4000).pubkey()
    owners = [sol_kp(seed, 0), sol_kp(seed, 1)]
    mint = Pubkey.from_string(USDT_SOL)
    tp = Pubkey.from_string(TOKEN_PROGRAM)
    nonce_acct, nonce_val = fake_key("nonce-account-1"), fake_key("nonce-value-1")

    def head(price):
        return [
            Instruction(Pubkey.from_string(SYSTEM), struct.pack("<I", 4), [
                AccountMeta(nonce_acct, False, True), AccountMeta(Pubkey.from_string(RECENT_BH), False, False),
                AccountMeta(treasury.pubkey(), True, False)]),
            Instruction(Pubkey.from_string(COMPUTE), bytes([2]) + struct.pack("<I", 200000), []),
            Instruction(Pubkey.from_string(COMPUTE), bytes([3]) + struct.pack("<Q", price), []),
        ]

    def transfer(src_owner, dst_owner, amount):
        return Instruction(tp, bytes([12]) + struct.pack("<Q", amount) + bytes([6]), [
            AccountMeta(ata(src_owner, USDT_SOL), False, True), AccountMeta(mint, False, False),
            AccountMeta(ata(dst_owner, USDT_SOL), False, True), AccountMeta(src_owner, True, False)])

    bh = Hash.from_bytes(bytes(nonce_val))
    # send: treasury -> dest
    send_ixs = head(1000) + [transfer(treasury.pubkey(), dest, 4300000)]
    send_tx = Transaction([treasury], Message.new_with_blockhash(send_ixs, treasury.pubkey(), bh), bh)
    # sweep: owners 0,1 -> treasury, create treasury USDT ATA
    create = Instruction(Pubkey.from_string(ATA_PROGRAM), bytes([1]), [
        AccountMeta(treasury.pubkey(), True, True), AccountMeta(ata(treasury.pubkey(), USDT_SOL), False, True),
        AccountMeta(treasury.pubkey(), False, False), AccountMeta(mint, False, False),
        AccountMeta(Pubkey.from_string(SYSTEM), False, False), AccountMeta(tp, False, False)])
    sweep_ixs = head(10000) + [create, transfer(owners[0].pubkey(), treasury.pubkey(), 4300000),
                               transfer(owners[1].pubkey(), treasury.pubkey(), 1)]
    sweep_tx = Transaction([treasury] + owners, Message.new_with_blockhash(sweep_ixs, treasury.pubkey(), bh), bh)
    import base64
    return {"treasury": str(treasury.pubkey()), "dest": str(dest),
            "send_raw": base64.b64encode(bytes(send_tx)).decode(), "sweep_raw": base64.b64encode(bytes(sweep_tx)).decode()}


if __name__ == "__main__":
    print(json.dumps({"evm": evm(), "solana": sol()}, indent=2))
