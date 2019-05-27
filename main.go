package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"lukechampine.com/us/wallet"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/flagg"
	"lukechampine.com/walrus"
)

var (
	// to be supplied at build time
	githash   = "?"
	builddate = "?"
)

var (
	rootUsage = `Usage:
    walrus-cli [flags] [action]

Actions:
    balance         view current balance
    addresses       list addresses
    addr            generate an address
    txn             create a transaction
    sign            sign a transaction
    broadcast       broadcast a transaction
`
	versionUsage = rootUsage
	balanceUsage = `Usage:
    walrus-cli balance

Reports the current balance.
`
	addressesUsage = `Usage:
    walrus-cli addresses

Lists addresses known to the wallet.
`
	addrUsage = `Usage:
    walrus-cli addr
    walrus-cli addr [key index]

Generates an address. If no key index is provided, the lowest unused key index
is used. The address is added to the wallet's set of tracked addresses.
`
	txnUsage = `Usage:
    walrus-cli txn [outputs] [file]

Creates a transaction sending containing the provided outputs, which are
specified as a comma-separated list of address:value pairs, where value is
specified in SC. The inputs are be selected automatically, and a change
address is generated if needed.
`
	signUsage = `Usage:
    walrus-cli sign [txn]

Signs the inputs of the provided transaction that the wallet controls.
`
	broadcastUsage = `Usage:
    walrus-cli broadcast [txn]

Broadcasts the provided transaction.
`
)

var usage = flagg.SimpleUsage(flagg.Root, rootUsage)

func check(err error, ctx string) {
	if err != nil {
		log.Fatalf("%v: %v", ctx, err)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func currencyUnits(c types.Currency) string {
	atto := types.NewCurrency64(1000000)
	if c.Cmp(atto) < 0 {
		return c.String() + " H"
	}
	mag := atto
	unit := ""
	for _, unit = range []string{"aS", "fS", "pS", "nS", "uS", "mS", "SC", "KS", "MS", "GS", "TS"} {
		if c.Cmp(mag.Mul64(1e3)) < 0 {
			break
		} else if unit != "TS" {
			mag = mag.Mul64(1e3)
		}
	}
	num := new(big.Rat).SetInt(c.Big())
	denom := new(big.Rat).SetInt(mag.Big())
	res, _ := new(big.Rat).Mul(num, denom.Inv(denom)).Float64()
	return fmt.Sprintf("%.4g %s", res, unit)
}

func readTxn(filename string) types.Transaction {
	js, err := ioutil.ReadFile(filename)
	check(err, "Could not read transaction file")
	var txn types.Transaction
	err = json.Unmarshal(js, &txn)
	check(err, "Could not parse transaction file")
	return txn
}

func writeTxn(filename string, txn types.Transaction) {
	js, _ := json.MarshalIndent(txn, "", "  ")
	js = append(js, '\n')
	err := ioutil.WriteFile(filename, js, 0666)
	check(err, "Could not write transaction to disk")
}

func main() {
	log.SetFlags(0)
	var sign, broadcast bool // used by txn and sign commands

	rootCmd := flagg.Root
	apiAddr := rootCmd.String("a", "localhost:9380", "host:port that the walrus API is running on")
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	versionCmd := flagg.New("version", versionUsage)
	balanceCmd := flagg.New("balance", balanceUsage)
	addressesCmd := flagg.New("addresses", addressesUsage)
	addrCmd := flagg.New("addr", addrUsage)
	txnCmd := flagg.New("txn", txnUsage)
	txnCmd.BoolVar(&sign, "sign", false, "sign the transaction")
	txnCmd.BoolVar(&broadcast, "broadcast", false, "broadcast the transaction")
	changeAddrStr := txnCmd.String("change", "", "use this change address instead of generating a new one")
	signCmd := flagg.New("sign", signUsage)
	signCmd.BoolVar(&broadcast, "broadcast", false, "broadcast the transaction (if true, omit file)")
	broadcastCmd := flagg.New("broadcast", broadcastUsage)

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
		Sub: []flagg.Tree{
			{Cmd: versionCmd},
			{Cmd: balanceCmd},
			{Cmd: addressesCmd},
			{Cmd: addrCmd},
			{Cmd: txnCmd},
			{Cmd: signCmd},
			{Cmd: broadcastCmd},
		},
	})
	args := cmd.Args()

	switch cmd {
	case rootCmd:
		if len(args) != 0 {
			cmd.Usage()
			return
		}
		fallthrough
	case versionCmd:
		log.Printf("walrus-cli v0.1.0\nCommit:     %s\nRelease:    %s\nGo version: %s %s/%s\nBuild Date: %s\n",
			githash, build.Release, runtime.Version(), runtime.GOOS, runtime.GOARCH, builddate)

	case balanceCmd:
		if len(args) != 0 {
			cmd.Usage()
			return
		}
		c := walrus.NewWatchSeedClient(*apiAddr)
		bal, err := c.Balance()
		check(err, "Could not get balance")
		fmt.Println(currencyUnits(bal))

	case addressesCmd:
		if len(args) != 0 {
			cmd.Usage()
			return
		}
		c := walrus.NewWatchSeedClient(*apiAddr)
		addrs, err := c.AllAddresses()
		check(err, "Could not get address list")
		for _, addr := range addrs {
			fmt.Println(addr)
		}

	case addrCmd:
		if len(args) > 1 {
			cmd.Usage()
			return
		}
		c := walrus.NewWatchSeedClient(*apiAddr)
		addrs, err := c.AllAddresses()
		check(err, "Could not get address list")
		var index uint64
		if len(args) == 0 {
			// use smallest unused index
			for _, addr := range addrs {
				addrInfo, err := c.AddressInfo(addr)
				check(err, "Could not get address info")
				if addrInfo.KeyIndex > index {
					index = addrInfo.KeyIndex
				}
			}
			index++
			fmt.Printf("No index specified; using lowest available index (%v)\n", index)
		} else {
			index, err = strconv.ParseUint(args[0], 10, 32)
			check(err, "Invalid index")
			// check for duplicate
			for _, addr := range addrs {
				addrInfo, err := c.AddressInfo(addr)
				check(err, "Could not get address info")
				if addrInfo.KeyIndex == index {
					fmt.Printf("WARNING: You have already generated an address with index %v.\n", index)
				}
			}
		}
		nanos, err := OpenNanoS()
		check(err, "Could not connect to Nano S")
		fmt.Printf("Please verify and accept the prompt on your device to generate address #%v.\n", index)
		addr, pubkey, err := nanos.GetAddress(uint32(index))
		check(err, "Could not generate address")
		fmt.Println("Compare the address displayed on your device to the address below:")
		fmt.Println("    " + addr.String())
		fmt.Print("Press ENTER to add this address to your wallet, or Ctrl-C to cancel.")
		bufio.NewReader(os.Stdin).ReadLine()
		addrInfo := wallet.SeedAddressInfo{
			UnlockConditions: types.UnlockConditions{
				PublicKeys:         []types.SiaPublicKey{pubkey},
				SignaturesRequired: 1,
			},
			KeyIndex: index,
		}
		err = c.WatchAddress(addrInfo)
		check(err, "Could not add address to wallet")
		fmt.Println("Address added successfully.")

	case txnCmd:
		if !((len(args) == 2) || (len(args) == 1 && broadcast)) {
			cmd.Usage()
			return
		}
		// parse outputs
		pairs := strings.Split(args[0], ",")
		outputs := make([]types.SiacoinOutput, len(pairs))
		var outputsSum types.Currency
		for i, p := range pairs {
			addrAmount := strings.Split(p, ":")
			if len(addrAmount) != 2 {
				check(errors.New("outputs must be specified in addr:amount pairs"), "Could not parse outputs")
			}
			err := outputs[i].UnlockHash.LoadString(strings.TrimSpace(addrAmount[0]))
			check(err, "Invalid destination address")
			amount, err := strconv.ParseFloat(strings.TrimSpace(addrAmount[1]), 64)
			check(err, "Invalid destination amount")
			outputs[i].Value = types.SiacoinPrecision.MulFloat(amount)
			outputsSum = outputsSum.Add(outputs[i].Value)
		}
		numOutputs := len(outputs)
		c := walrus.NewWatchSeedClient(*apiAddr)
		utxos, err := c.UnspentOutputs()
		check(err, "Could not get utxos")
		inputs := make([]wallet.ValuedInput, len(utxos))
		for i := range utxos {
			inputs[i] = wallet.ValuedInput{
				SiacoinInput: types.SiacoinInput{
					ParentID:         utxos[i].ID,
					UnlockConditions: utxos[i].UnlockConditions,
				},
				Value: utxos[i].Value,
			}
		}
		feePerByte, err := c.RecommendedFee()
		check(err, "Could not get recommended transaction fee")
		used, fee, change, ok := wallet.FundTransaction(outputsSum, feePerByte, inputs)
		if !ok {
			check(errors.New("insufficient funds"), "Could not create transaction")
		}
		var nanos *NanoS
		if !change.IsZero() {
			var changeAddr types.UnlockHash
			if *changeAddrStr != "" {
				err = changeAddr.LoadString(*changeAddrStr)
				check(err, "Could not parse change address")
			} else {
				fmt.Println("This transaction requires a 'change output' that will send excess coins back to your wallet.")
				fmt.Println("Please verify and accept the prompt on your device to generate a change address.")
				fmt.Println("(You may use the --change flag to specify a change address in advance.)")
				addrs, err := c.AllAddresses()
				check(err, "Could not get address list")
				var index uint64
				for _, addr := range addrs {
					addrInfo, err := c.AddressInfo(addr)
					check(err, "Could not get address info")
					if addrInfo.KeyIndex > index {
						index = addrInfo.KeyIndex
					}
				}
				index++
				nanos, err = OpenNanoS()
				check(err, "Could not connect to Nano S")
				var pubkey types.SiaPublicKey
				changeAddr, pubkey, err = nanos.GetAddress(uint32(index))
				check(err, "Could not generate address")
				fmt.Println("Compare the address displayed on your device to the address below:")
				fmt.Println("    " + changeAddr.String())
				fmt.Print("Press ENTER to add this address to your wallet, or Ctrl-C to cancel.")
				bufio.NewReader(os.Stdin).ReadLine()
				addrInfo := wallet.SeedAddressInfo{
					UnlockConditions: types.UnlockConditions{
						PublicKeys:         []types.SiaPublicKey{pubkey},
						SignaturesRequired: 1,
					},
					KeyIndex: index,
				}
				err = c.WatchAddress(addrInfo)
				check(err, "Could not add address to wallet")
				fmt.Println("Change address added successfully.")
				fmt.Println()
			}
			outputs = append(outputs, types.SiacoinOutput{
				Value:      change,
				UnlockHash: changeAddr,
			})
		}
		txn := types.Transaction{
			SiacoinInputs:  make([]types.SiacoinInput, len(used)),
			SiacoinOutputs: outputs,
			MinerFees:      []types.Currency{fee},
		}
		var inputSum types.Currency
		for i, in := range used {
			txn.SiacoinInputs[i] = in.SiacoinInput
			inputSum = inputSum.Add(in.Value)
		}
		check(err, "Could not write transaction to disk")
		fmt.Println("Transaction summary:")
		fmt.Printf("- %v input%v, totalling %v\n", len(used), plural(len(used)), currencyUnits(inputSum))
		fmt.Printf("- %v output%v, totalling %v\n", numOutputs, plural(numOutputs), currencyUnits(outputsSum))
		if !change.IsZero() {
			fmt.Printf(" (plus a change output, sending %v back to your wallet)\n", currencyUnits(change))
		}
		fmt.Printf("- A miner fee of %v, which is %v/byte\n", currencyUnits(fee), currencyUnits(feePerByte))
		fmt.Println()

		if sign {
			if nanos == nil {
				nanos, err = OpenNanoS()
				check(err, "Could not connect to Nano S")
			}
			err := signFlow(c, nanos, &txn)
			check(err, "Could not sign transaction")
		} else {
			fmt.Println("Transaction has not been signed. You can sign it with the 'sign' command.")
		}

		if broadcast {
			err := broadcastFlow(c, txn)
			check(err, "Could not broadcast transaction")
			return
		}

		writeTxn(args[1], txn)
		if sign {
			fmt.Println("Wrote signed transaction to", args[1])
		} else {
			fmt.Println("Wrote unsigned transaction to", args[1])
		}

	case signCmd:
		if len(args) != 1 {
			cmd.Usage()
			return
		}
		txn := readTxn(args[0])
		c := walrus.NewWatchSeedClient(*apiAddr)
		nanos, err := OpenNanoS()
		check(err, "Could not connect to Nano S")

		err = signFlow(c, nanos, &txn)
		check(err, "Could not sign transaction")

		if broadcast {
			err := broadcastFlow(c, txn)
			check(err, "Could not broadcast transaction")
		} else {
			ext := filepath.Ext(args[0])
			signedPath := strings.TrimSuffix(args[0], ext) + "-signed" + ext
			writeTxn(signedPath, txn)
			fmt.Println("Wrote signed transaction to", signedPath+".")
			fmt.Println("You can now use the 'broadcast' command to broadcast this transaction.")
		}

	case broadcastCmd:
		if len(args) != 1 {
			cmd.Usage()
			return
		}
		err := broadcastFlow(walrus.NewWatchSeedClient(*apiAddr), readTxn(args[0]))
		check(err, "Could not broadcast transaction")
	}
}

func broadcastFlow(c *walrus.WatchSeedClient, txn types.Transaction) error {
	err := c.Broadcast([]types.Transaction{txn})
	if err != nil {
		return err
	}
	fmt.Println("Transaction broadcast successfully.")
	fmt.Println("Transaction ID:", txn.ID())
	return nil
}

func signFlow(c *walrus.WatchSeedClient, nanos *NanoS, txn *types.Transaction) error {
	addrs, err := c.AllAddresses()
	check(err, "Could not get addresses")
	addrMap := make(map[types.UnlockHash]struct{})
	for _, addr := range addrs {
		addrMap[addr] = struct{}{}
	}
	sigMap := make(map[int]uint64)
	for _, in := range txn.SiacoinInputs {
		addr := in.UnlockConditions.UnlockHash()
		if _, ok := addrMap[addr]; ok {
			// get key index
			info, err := c.AddressInfo(addr)
			check(err, "Could not get address info")
			// add signature entry
			sig := wallet.StandardTransactionSignature(crypto.Hash(in.ParentID))
			txn.TransactionSignatures = append(txn.TransactionSignatures, sig)
			sigMap[len(txn.TransactionSignatures)-1] = info.KeyIndex
			continue
		}
	}
	if len(sigMap) == 0 {
		fmt.Println("Nothing to sign: transaction does not spend any outputs recognized by this wallet")
		return nil
	}
	// request signatures from device
	fmt.Println("Please verify the transaction details on your device. You should see:")
	for _, sco := range txn.SiacoinOutputs {
		r := new(big.Rat).SetFrac(sco.Value.Big(), types.SiacoinPrecision.Big())
		fmt.Println("   ", sco.UnlockHash, "receiving", r.FloatString(5), "SC")
	}
	for _, fee := range txn.MinerFees {
		r := new(big.Rat).SetFrac(fee.Big(), types.SiacoinPrecision.Big())
		fmt.Println("    A miner fee of", r.FloatString(5), "SC")
	}
	if len(sigMap) > 1 {
		fmt.Printf("Each signature must be completed separately, so you will be prompted %v times.\n", len(sigMap))
	}
	for sigIndex, keyIndex := range sigMap {
		fmt.Printf("Waiting for signature for input %v, key %v...", sigIndex, keyIndex)
		sig, err := nanos.SignTxn(*txn, uint16(sigIndex), uint32(keyIndex))
		check(err, "Could not get signature")
		txn.TransactionSignatures[sigIndex].Signature = sig[:]
		fmt.Println("Done")
	}
	return nil
}