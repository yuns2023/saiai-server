package hdkey

import (
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// BIP32 test vectors copied verbatim from
// https://github.com/bitcoin/bips/blob/master/bip-0032.mediawiki (fetched 2026-11-16).
type bip32Step struct {
	path string
	pub  string
	prv  string
}

var bip32Vectors = []struct {
	seed  string
	steps []bip32Step
}{
	{
		seed: "000102030405060708090a0b0c0d0e0f",
		steps: []bip32Step{
			{"m", "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8", "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"},
			{"m/0'", "xpub68Gmy5EdvgibQVfPdqkBBCHxA5htiqg55crXYuXoQRKfDBFA1WEjWgP6LHhwBZeNK1VTsfTFUHCdrfp1bgwQ9xv5ski8PX9rL2dZXvgGDnw", "xprv9uHRZZhk6KAJC1avXpDAp4MDc3sQKNxDiPvvkX8Br5ngLNv1TxvUxt4cV1rGL5hj6KCesnDYUhd7oWgT11eZG7XnxHrnYeSvkzY7d2bhkJ7"},
			{"m/0'/1", "xpub6ASuArnXKPbfEwhqN6e3mwBcDTgzisQN1wXN9BJcM47sSikHjJf3UFHKkNAWbWMiGj7Wf5uMash7SyYq527Hqck2AxYysAA7xmALppuCkwQ", "xprv9wTYmMFdV23N2TdNG573QoEsfRrWKQgWeibmLntzniatZvR9BmLnvSxqu53Kw1UmYPxLgboyZQaXwTCg8MSY3H2EU4pWcQDnRnrVA1xe8fs"},
			{"m/0'/1/2'", "xpub6D4BDPcP2GT577Vvch3R8wDkScZWzQzMMUm3PWbmWvVJrZwQY4VUNgqFJPMM3No2dFDFGTsxxpG5uJh7n7epu4trkrX7x7DogT5Uv6fcLW5", "xprv9z4pot5VBttmtdRTWfWQmoH1taj2axGVzFqSb8C9xaxKymcFzXBDptWmT7FwuEzG3ryjH4ktypQSAewRiNMjANTtpgP4mLTj34bhnZX7UiM"},
			{"m/0'/1/2'/2", "xpub6FHa3pjLCk84BayeJxFW2SP4XRrFd1JYnxeLeU8EqN3vDfZmbqBqaGJAyiLjTAwm6ZLRQUMv1ZACTj37sR62cfN7fe5JnJ7dh8zL4fiyLHV", "xprvA2JDeKCSNNZky6uBCviVfJSKyQ1mDYahRjijr5idH2WwLsEd4Hsb2Tyh8RfQMuPh7f7RtyzTtdrbdqqsunu5Mm3wDvUAKRHSC34sJ7in334"},
			{"m/0'/1/2'/2/1000000000", "xpub6H1LXWLaKsWFhvm6RVpEL9P4KfRZSW7abD2ttkWP3SSQvnyA8FSVqNTEcYFgJS2UaFcxupHiYkro49S8yGasTvXEYBVPamhGW6cFJodrTHy", "xprvA41z7zogVVwxVSgdKUHDy1SKmdb533PjDz7J6N6mV6uS3ze1ai8FHa8kmHScGpWmj4WggLyQjgPie1rFSruoUihUZREPSL39UNdE3BBDu76"},
		},
	},
	{
		seed: "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
		steps: []bip32Step{
			{"m", "xpub661MyMwAqRbcFW31YEwpkMuc5THy2PSt5bDMsktWQcFF8syAmRUapSCGu8ED9W6oDMSgv6Zz8idoc4a6mr8BDzTJY47LJhkJ8UB7WEGuduB", "xprv9s21ZrQH143K31xYSDQpPDxsXRTUcvj2iNHm5NUtrGiGG5e2DtALGdso3pGz6ssrdK4PFmM8NSpSBHNqPqm55Qn3LqFtT2emdEXVYsCzC2U"},
			{"m/0", "xpub69H7F5d8KSRgmmdJg2KhpAK8SR3DjMwAdkxj3ZuxV27CprR9LgpeyGmXUbC6wb7ERfvrnKZjXoUmmDznezpbZb7ap6r1D3tgFxHmwMkQTPH", "xprv9vHkqa6EV4sPZHYqZznhT2NPtPCjKuDKGY38FBWLvgaDx45zo9WQRUT3dKYnjwih2yJD9mkrocEZXo1ex8G81dwSM1fwqWpWkeS3v86pgKt"},
			{"m/0/2147483647'", "xpub6ASAVgeehLbnwdqV6UKMHVzgqAG8Gr6riv3Fxxpj8ksbH9ebxaEyBLZ85ySDhKiLDBrQSARLq1uNRts8RuJiHjaDMBU4Zn9h8LZNnBC5y4a", "xprv9wSp6B7kry3Vj9m1zSnLvN3xH8RdsPP1Mh7fAaR7aRLcQMKTR2vidYEeEg2mUCTAwCd6vnxVrcjfy2kRgVsFawNzmjuHc2YmYRmagcEPdU9"},
			{"m/0/2147483647'/1", "xpub6DF8uhdarytz3FWdA8TvFSvvAh8dP3283MY7p2V4SeE2wyWmG5mg5EwVvmdMVCQcoNJxGoWaU9DCWh89LojfZ537wTfunKau47EL2dhHKon", "xprv9zFnWC6h2cLgpmSA46vutJzBcfJ8yaJGg8cX1e5StJh45BBciYTRXSd25UEPVuesF9yog62tGAQtHjXajPPdbRCHuWS6T8XA2ECKADdw4Ef"},
			{"m/0/2147483647'/1/2147483646'", "xpub6ERApfZwUNrhLCkDtcHTcxd75RbzS1ed54G1LkBUHQVHQKqhMkhgbmJbZRkrgZw4koxb5JaHWkY4ALHY2grBGRjaDMzQLcgJvLJuZZvRcEL", "xprvA1RpRA33e1JQ7ifknakTFpgNXPmW2YvmhqLQYMmrj4xJXXWYpDPS3xz7iAxn8L39njGVyuoseXzU6rcxFLJ8HFsTjSyQbLYnMpCqE2VbFWc"},
			{"m/0/2147483647'/1/2147483646'/2", "xpub6FnCn6nSzZAw5Tw7cgR9bi15UV96gLZhjDstkXXxvCLsUXBGXPdSnLFbdpq8p9HmGsApME5hQTZ3emM2rnY5agb9rXpVGyy3bdW6EEgAtqt", "xprvA2nrNbFZABcdryreWet9Ea4LvTJcGsqrMzxHx98MMrotbir7yrKCEXw7nadnHM8Dq38EGfSh6dqA9QWTyefMLEcBYJUuekgW4BYPJcr9E7j"},
		},
	},
	{
		// Vector 3: retention of leading zeros.
		seed: "4b381541583be4423346c643850da4b320e46a87ae3d2a4e6da11eba819cd4acba45d239319ac14f863b8d5ab5a0d0c64d2e8a1e7d1457df2e5a3c51c73235be",
		steps: []bip32Step{
			{"m", "xpub661MyMwAqRbcEZVB4dScxMAdx6d4nFc9nvyvH3v4gJL378CSRZiYmhRoP7mBy6gSPSCYk6SzXPTf3ND1cZAceL7SfJ1Z3GC8vBgp2epUt13", "xprv9s21ZrQH143K25QhxbucbDDuQ4naNntJRi4KUfWT7xo4EKsHt2QJDu7KXp1A3u7Bi1j8ph3EGsZ9Xvz9dGuVrtHHs7pXeTzjuxBrCmmhgC6"},
			{"m/0'", "xpub68NZiKmJWnxxS6aaHmn81bvJeTESw724CRDs6HbuccFQN9Ku14VQrADWgqbhhTHBaohPX4CjNLf9fq9MYo6oDaPPLPxSb7gwQN3ih19Zm4Y", "xprv9uPDJpEQgRQfDcW7BkF7eTya6RPxXeJCqCJGHuCJ4GiRVLzkTXBAJMu2qaMWPrS7AANYqdq6vcBcBUdJCVVFceUvJFjaPdGZ2y9WACViL4L"},
		},
	},
	{
		// Vector 4: retention of leading zeros in hardened derivation.
		seed: "3ddd5602285899a946114506157c7997e5444528f3003f6134712147db19b678",
		steps: []bip32Step{
			{"m", "xpub661MyMwAqRbcGczjuMoRm6dXaLDEhW1u34gKenbeYqAix21mdUKJyuyu5F1rzYGVxyL6tmgBUAEPrEz92mBXjByMRiJdba9wpnN37RLLAXa", "xprv9s21ZrQH143K48vGoLGRPxgo2JNkJ3J3fqkirQC2zVdk5Dgd5w14S7fRDyHH4dWNHUgkvsvNDCkvAwcSHNAQwhwgNMgZhLtQC63zxwhQmRv"},
			{"m/0'", "xpub69AUMk3qDBi3uW1sXgjCmVjJ2G6WQoYSnNHyzkmdCHEhSZ4tBok37xfFEqHd2AddP56Tqp4o56AePAgCjYdvpW2PU2jbUPFKsav5ut6Ch1m", "xprv9vB7xEWwNp9kh1wQRfCCQMnZUEG21LpbR9NPCNN1dwhiZkjjeGRnaALmPXCX7SgjFTiCTT6bXes17boXtjq3xLpcDjzEuGLQBM5ohqkao9G"},
			{"m/0'/1'", "xpub6BJA1jSqiukeaesWfxe6sNK9CCGaujFFSJLomWHprUL9DePQ4JDkM5d88n49sMGJxrhpjazuXYWdMf17C9T5XnxkopaeS7jGk1GyyVziaMt", "xprv9xJocDuwtYCMNAo3Zw76WENQeAS6WGXQ55RCy7tDJ8oALr4FWkuVoHJeHVAcAqiZLE7Je3vZJHxspZdFHfnBEjHqU5hG1Jaj32dVoS6XLT1"},
		},
	},
}

func TestBIP32PrivateVectors(t *testing.T) {
	for vi, v := range bip32Vectors {
		seed, _ := hex.DecodeString(v.seed)
		master, err := NewMasterKey(seed)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.steps {
			k, err := master.DerivePath(s.path)
			if err != nil {
				t.Fatalf("vector %d %s: %v", vi+1, s.path, err)
			}
			if got := k.String(); got != s.prv {
				t.Errorf("vector %d %s prv\n got %s\nwant %s", vi+1, s.path, got, s.prv)
			}
			if got := k.Neuter().String(); got != s.pub {
				t.Errorf("vector %d %s pub\n got %s\nwant %s", vi+1, s.path, got, s.pub)
			}
		}
	}
}

// Every non-hardened step in the vectors must also be reachable via CKDpub
// from the parent xpub alone (this is what the server does).
func TestBIP32PublicDerivationVectors(t *testing.T) {
	checked := 0
	for vi, v := range bip32Vectors {
		for i := 1; i < len(v.steps); i++ {
			idx, err := ParsePath(v.steps[i].path)
			if err != nil {
				t.Fatal(err)
			}
			last := idx[len(idx)-1]
			parent, err := ParseExtendedPublicKey(v.steps[i-1].pub)
			if err != nil {
				t.Fatalf("vector %d parse %s: %v", vi+1, v.steps[i-1].path, err)
			}
			child, err := parent.PublicChild(last)
			if last >= HardenedOffset {
				if !errors.Is(err, ErrHardenedPublic) {
					t.Fatalf("hardened public derivation must fail, got %v", err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := child.String(); got != v.steps[i].pub {
				t.Errorf("vector %d %s CKDpub\n got %s\nwant %s", vi+1, v.steps[i].path, got, v.steps[i].pub)
			}
			checked++
		}
	}
	if checked != 6 {
		t.Fatalf("expected 6 public steps checked, got %d", checked)
	}
}

func TestParseExtendedPublicKeyRejects(t *testing.T) {
	// From BIP32 test vector 5 plus a valid xprv from vector 1.
	cases := map[string]string{
		"xpub661MyMwAqRbcEYS8w7XLSVeEsBXy79zSzH1J8vCdxAZningWLdN3zgtU6LBpB85b3D2yc8sfvZU521AAwdZafEz7mnzBBsz4wKY5fTtTQBm": "pubkey version / prvkey mismatch",
		"xpub661MyMwAqRbcEYS8w7XLSVeEsBXy79zSzH1J8vCdxAZningWLdN3zgtU6Txnt3siSujt9RCVYsx4qHZGc62TG4McvMGcAUjeuwZdduYEvFn": "invalid pubkey prefix 04",
		"xpub661MyMwAqRbcEYS8w7XLSVeEsBXy79zSzH1J8vCdxAZningWLdN3zgtU6N8ZMMXctdiCjxTNq964yKkwrkBJJwpzZS4HS2fxvyYUA4q2Xe4": "invalid pubkey prefix 01",
		"xpub661no6RGEX3uJkY4bNnPcw4URcQTrSibUZ4NqJEw5eBkv7ovTwgiT91XX27VbEXGENhYRCf7hyEbWrR3FewATdCEebj6znwMfQkhRYHRLpJ": "zero depth with non-zero parent fingerprint",
		"xpub661MyMwAuDcm6CRQ5N4qiHKrJ39Xe1R1NyfouMKTTWcguwVcfrZJaNvhpebzGerh7gucBvzEQWRugZDuDXjNDRmXzSZe4c7mnTK97pTvGS8": "zero depth with non-zero index",
		"DMwo58pR1QLEFihHiXPVykYB6fJmsTeHvyTp7hRThAtCX8CvYzgPcn8XnmdfHGMQzT7ayAmfo4z3gY5KfbrZWZ6St24UVf2Qgo6oujFktLHdHY4": "unknown extended key version",
		"xpub661MyMwAqRbcEYS8w7XLSVeEsBXy79zSzH1J8vCdxAZningWLdN3zgtU6Q5JXayek4PRsn35jii4veMimro1xefsM58PgBMrvdYre8QyULY": "invalid pubkey 02..07",
		"xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHL": "invalid checksum",
		"xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi": "private key",
		"": "empty",
	}
	for s, why := range cases {
		if _, err := ParseExtendedPublicKey(s); err == nil {
			t.Errorf("%s: expected rejection of %q", why, s)
		}
	}
	_, err := ParseExtendedPublicKey("xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi")
	if !errors.Is(err, ErrPrivateKeyGiven) {
		t.Fatalf("xprv must be rejected with ErrPrivateKeyGiven, got %v", err)
	}
}

// SLIP-10 ed25519 vectors copied verbatim from
// https://github.com/satoshilabs/slips/blob/master/slip-0010.md (fetched 2026-11-16).
func TestSLIP10Ed25519Vectors(t *testing.T) {
	type step struct{ path, chain, priv, pub string }
	vectors := []struct {
		seed  string
		steps []step
	}{
		{"000102030405060708090a0b0c0d0e0f", []step{
			{"m", "90046a93de5380a72b5e45010748567d5ea02bbf6522f979e05c0d8d8ca9fffb", "2b4be7f19ee27bbf30c667b642d5f4aa69fd169872f8fc3059c08ebae2eb19e7", "00a4b2856bfec510abab89753fac1ac0e1112364e7d250545963f135f2a33188ed"},
			{"m/0'", "8b59aa11380b624e81507a27fedda59fea6d0b779a778918a2fd3590e16e9c69", "68e0fe46dfb67e368c75379acec591dad19df3cde26e63b93a8e704f1dade7a3", "008c8a13df77a28f3445213a0f432fde644acaa215fc72dcdf300d5efaa85d350c"},
			{"m/0'/1'", "a320425f77d1b5c2505a6b1b27382b37368ee640e3557c315416801243552f14", "b1d0bad404bf35da785a64ca1ac54b2617211d2777696fbffaf208f746ae84f2", "001932a5270f335bed617d5b935c80aedb1a35bd9fc1e31acafd5372c30f5c1187"},
			{"m/0'/1'/2'", "2e69929e00b5ab250f49c3fb1c12f252de4fed2c1db88387094a0f8c4c9ccd6c", "92a5b23c0b8a99e37d07df3fb9966917f5d06e02ddbd909c7e184371463e9fc9", "00ae98736566d30ed0e9d2f4486a64bc95740d89c7db33f52121f8ea8f76ff0fc1"},
			{"m/0'/1'/2'/2'", "8f6d87f93d750e0efccda017d662a1b31a266e4a6f5993b15f5c1f07f74dd5cc", "30d1dc7e5fc04c31219ab25a27ae00b50f6fd66622f6e9c913253d6511d1e662", "008abae2d66361c879b900d204ad2cc4984fa2aa344dd7ddc46007329ac76c429c"},
			{"m/0'/1'/2'/2'/1000000000'", "68789923a0cac2cd5a29172a475fe9e0fb14cd6adb5ad98a3fa70333e7afa230", "8f94d394a8e8fd6b1bc2f3f49f5c47e385281d5c17e65324b0f62483e37e8793", "003c24da049451555d51a7014a37337aa4e12d41e485abccfa46b47dfb2af54b7a"},
		}},
		{"fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542", []step{
			{"m", "ef70a74db9c3a5af931b5fe73ed8e1a53464133654fd55e7a66f8570b8e33c3b", "171cb88b1b3c1db25add599712e36245d75bc65a1a5c9e18d76f9f2b1eab4012", "008fe9693f8fa62a4305a140b9764c5ee01e455963744fe18204b4fb948249308a"},
			{"m/0'", "0b78a3226f915c082bf118f83618a618ab6dec793752624cbeb622acb562862d", "1559eb2bbec5790b0c65d8693e4d0875b1747f4970ae8b650486ed7470845635", "0086fab68dcb57aa196c77c5f264f215a112c22a912c10d123b0d03c3c28ef1037"},
			{"m/0'/2147483647'", "138f0b2551bcafeca6ff2aa88ba8ed0ed8de070841f0c4ef0165df8181eaad7f", "ea4f5bfe8694d8bb74b7b59404632fd5968b774ed545e810de9c32a4fb4192f4", "005ba3b9ac6e90e83effcd25ac4e58a1365a9e35a3d3ae5eb07b9e4d90bcf7506d"},
			{"m/0'/2147483647'/1'", "73bd9fff1cfbde33a1b846c27085f711c0fe2d66fd32e139d3ebc28e5a4a6b90", "3757c7577170179c7868353ada796c839135b3d30554bbb74a4b1e4a5a58505c", "002e66aa57069c86cc18249aecf5cb5a9cebbfd6fadeab056254763874a9352b45"},
			{"m/0'/2147483647'/1'/2147483646'", "0902fe8a29f9140480a00ef244bd183e8a13288e4412d8389d140aac1794825a", "5837736c89570de861ebc173b1086da4f505d4adb387c6a1b1342d5e4ac9ec72", "00e33c0f7d81d843c572275f287498e8d408654fdf0d1e065b84e2e6f157aab09b"},
			{"m/0'/2147483647'/1'/2147483646'/2'", "5d70af781f3a37b829f0d060924d5e960bdc02e85423494afc0b1a41bbe196d4", "551d333177df541ad876a60ea71f00447931c0a9da16f227c11ea080d7391b8d", "0047150c75db263559a70d5778bf36abbab30fb061ad69f69ece61a72b0cfa4fc0"},
		}},
	}
	for vi, v := range vectors {
		seed, _ := hex.DecodeString(v.seed)
		master := NewSLIP10Ed25519Master(seed)
		for _, s := range v.steps {
			k, err := master.DerivePath(s.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(k.ChainCode[:]); got != s.chain {
				t.Errorf("v%d %s chain %s want %s", vi+1, s.path, got, s.chain)
			}
			if got := hex.EncodeToString(k.Key[:]); got != s.priv {
				t.Errorf("v%d %s priv %s want %s", vi+1, s.path, got, s.priv)
			}
			if got := "00" + hex.EncodeToString(k.PublicKey()); got != s.pub {
				t.Errorf("v%d %s pub %s want %s", vi+1, s.path, got, s.pub)
			}
		}
	}
	if _, err := NewSLIP10Ed25519Master([]byte("0123456789abcdef")).DerivePath("m/44'/501'/0"); err == nil {
		t.Fatal("non-hardened ed25519 component must be rejected")
	}
}

func TestParsePath(t *testing.T) {
	got, err := ParsePath("m/44'/60'/0'/0/7")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{44 + HardenedOffset, 60 + HardenedOffset, HardenedOffset, 0, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	for _, bad := range []string{"", "44/0", "m/x", "m/2147483648", "m/-1"} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

// secp256k1 group order n (SEC 2, also secp256k1.Params().N).
func groupOrderBytes() []byte {
	b := secp256k1.Params().N.Bytes()
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func TestBIP32InvalidChildBranches(t *testing.T) {
	ir := make([]byte, 32)
	n := groupOrderBytes()
	// Master: IL = n and IL = 0 are invalid.
	if _, err := masterFromIL(n, ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("master IL=n: %v", err)
	}
	if _, err := masterFromIL(make([]byte, 32), ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("master IL=0: %v", err)
	}

	// Parent private key k = 7; IL = n - 7 makes child key 0 and child point infinity.
	parent, err := masterFromIL(append(make([]byte, 31), 7), ir)
	if err != nil {
		t.Fatal(err)
	}
	pub := parent.Neuter()
	nMinus7 := new(big.Int).Sub(secp256k1.Params().N, big.NewInt(7)).FillBytes(make([]byte, 32))
	parentPub := pub.PubKey.SerializeCompressed()

	if _, err := parent.childFromIL(0, parentPub, nMinus7, ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("zero child private key: %v", err)
	}
	if _, err := pub.publicChildFromIL(0, parentPub, nMinus7, ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("point at infinity: %v", err)
	}
	// IL >= n on both paths.
	if _, err := parent.childFromIL(0, parentPub, n, ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("priv IL=n: %v", err)
	}
	if _, err := pub.publicChildFromIL(0, parentPub, n, ir); !errors.Is(err, ErrInvalidChild) {
		t.Fatalf("pub IL=n: %v", err)
	}
	// A valid IL on both paths yields the same public key.
	one := append(make([]byte, 31), 1)
	c1, err := parent.childFromIL(0, parentPub, one, ir)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := pub.publicChildFromIL(0, parentPub, one, ir)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Neuter().String() != c2.String() {
		t.Fatal("private/public child mismatch")
	}
}
