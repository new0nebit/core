package rhp

import (
	"math/bits"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

// ContractRenewalCost returns the cost of renewing a contract for the renter.
// In other words, this is the amount of money that the renter needs to fund the
// contract txn with.
func ContractRenewalCost(cs consensus.State, pt HostPriceTable, fc types.FileContract, minerFee, basePrice types.Currency) types.Currency {
	return fc.ValidRenterPayout().Add(pt.ContractPrice).Add(minerFee).Add(basePrice).Add(cs.FileContractTax(fc))
}

// PrepareContractRenewal constructs a contract renewal transaction.
func PrepareContractRenewal(currentRevision types.FileContractRevision, hostAddress, renterAddress types.Address, renterKey types.PrivateKey, renterPayout, newCollateral types.Currency, hostKey types.PublicKey, pt HostPriceTable, endHeight uint64) (types.FileContract, types.Currency) {
	hostValidPayout, hostMissedPayout, voidMissedPayout, basePrice := CalculateHostPayouts(currentRevision.FileContract, newCollateral, pt, endHeight)

	return types.FileContract{
		Filesize:       currentRevision.Filesize,
		FileMerkleRoot: currentRevision.FileMerkleRoot,
		WindowStart:    uint64(endHeight),
		WindowEnd:      uint64(endHeight + pt.WindowSize),
		Payout:         taxAdjustedPayout(renterPayout.Add(hostValidPayout)),
		UnlockHash:     currentRevision.UnlockHash,
		RevisionNumber: 0,
		ValidProofOutputs: []types.SiacoinOutput{
			{Value: renterPayout, Address: renterAddress},
			{Value: hostValidPayout, Address: hostAddress},
		},
		MissedProofOutputs: []types.SiacoinOutput{
			{Value: renterPayout, Address: renterAddress},
			{Value: hostMissedPayout, Address: hostAddress},
			{Value: voidMissedPayout, Address: types.Address{}},
		},
	}, basePrice
}

// CalculateHostPayouts calculates the contract payouts for the host.
func CalculateHostPayouts(fc types.FileContract, newCollateral types.Currency, pt HostPriceTable, endHeight uint64) (types.Currency, types.Currency, types.Currency, types.Currency) {
	// Calculate the base price and base collateral. The price always includes
	// the fee for renewing the contract.
	basePrice := pt.RenewContractCost
	var baseCollateral types.Currency

	// if the contract height did not increase both prices are zero
	if contractEnd := uint64(endHeight + pt.WindowSize); contractEnd > fc.WindowEnd {
		timeExtension := uint64(contractEnd - fc.WindowEnd)
		basePrice = basePrice.Add(pt.WriteStoreCost.Mul64(fc.Filesize).Mul64(timeExtension))
		baseCollateral = baseCollateral.Add(pt.CollateralCost.Mul64(fc.Filesize).Mul64(timeExtension))
	}

	// calculate payouts
	hostValidPayout := pt.ContractPrice.Add(basePrice).Add(baseCollateral).Add(newCollateral)
	voidMissedPayout := basePrice.Add(baseCollateral)
	if hostValidPayout.Cmp(voidMissedPayout) < 0 {
		// TODO: detect this elsewhere
		panic("host's settings are unsatisfiable")
	}
	hostMissedPayout := hostValidPayout.Sub(voidMissedPayout)
	return hostValidPayout, hostMissedPayout, voidMissedPayout, basePrice
}

// NOTE: due to a bug in the transaction validation code, calculating payouts
// is way harder than it needs to be. Tax is calculated on the post-tax
// contract payout (instead of the sum of the renter and host payouts). So the
// equation for the payout is:
//
//	   payout = renterPayout + hostPayout + payout*tax
//	∴  payout = (renterPayout + hostPayout) / (1 - tax)
//
// This would work if 'tax' were a simple fraction, but because the tax must
// be evenly distributed among siafund holders, 'tax' is actually a function
// that multiplies by a fraction and then rounds down to the nearest multiple
// of the siafund count. Thus, when inverting the function, we have to make an
// initial guess and then fix the rounding error.
func taxAdjustedPayout(target types.Currency) types.Currency {
	// compute initial guess as target * (1 / 1-tax); since this does not take
	// the siafund rounding into account, the guess will be up to
	// types.SiafundCount greater than the actual payout value.
	guess := target.Mul64(1000).Div64(961)

	// now, adjust the guess to remove the rounding error. We know that:
	//
	//   (target % types.SiafundCount) == (payout % types.SiafundCount)
	//
	// therefore, we can simply adjust the guess to have this remainder as
	// well. The only wrinkle is that, since we know guess >= payout, if the
	// guess remainder is smaller than the target remainder, we must subtract
	// an extra types.SiafundCount.
	//
	// for example, if target = 87654321 and types.SiafundCount = 10000, then:
	//
	//   initial_guess  = 87654321 * (1 / (1 - tax))
	//                  = 91211572
	//   target % 10000 =     4321
	//   adjusted_guess = 91204321

	mod64 := func(c types.Currency, v uint64) types.Currency {
		var r uint64
		if c.Hi < v {
			_, r = bits.Div64(c.Hi, c.Lo, v)
		} else {
			_, r = bits.Div64(0, c.Hi, v)
			_, r = bits.Div64(r, c.Lo, v)
		}
		return types.NewCurrency64(r)
	}
	sfc := (consensus.State{}).SiafundCount()
	tm := mod64(target, sfc)
	gm := mod64(guess, sfc)
	if gm.Cmp(tm) < 0 {
		guess = guess.Sub(types.NewCurrency64(sfc))
	}
	return guess.Add(tm).Sub(gm)
}
