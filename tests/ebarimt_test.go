package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/batorgil-it/ebarimt-pos3-go/constants"
	ebarimt3SdkServices "github.com/batorgil-it/ebarimt-pos3-go/services"
	"github.com/batorgil-it/ebarimt-pos3-go/structs"
	"github.com/batorgil-it/ebarimt-pos3-go/utils"
	"github.com/stretchr/testify/assert"
)

var items = []structs.CreateItemInputModel{{
	Name:               "VAT & VAT ZERO & VAT FREE & NO VAT",
	TaxType:            constants.TAX_VAT_ABLE,
	ClassificationCode: "2441030",
	Qty:                1,
	IsCityTax:          false,
	MeasureUnit:        "unit",
	TotalAmount:        10,
	TaxProductCode:     "447",
},
}

func TestAmounts(t *testing.T) {
	assert := assert.New(t)

	totalVat := 0.0

	for _, item := range items {
		if item.TaxType == constants.TAX_NO_VAT {
			continue
		}

		if item.TaxType == constants.TAX_VAT_ABLE {
			if item.IsCityTax {
				totalVat += utils.GetVatWithCityTax(item.TotalAmount)
			} else {
				totalVat += utils.GetVat(item.TotalAmount)
			}
		}
	}

	assert.Equal(7.53, utils.NumberPrecision(totalVat), "GetVat func is not correct")
}

func TestGetBranchInfo(t *testing.T) {
	assert := assert.New(t)

	sdk := NewSdk()

	res, err := sdk.GetBranchInfo()
	assert.Nil(err, fmt.Sprintf("Ebarimt Error : %v ", res.Msg))

	assert.Equal(constants.POS_STATUS_SUCCESS, res.Status, "Ebarimt Error : %v", res.Msg)

	for _, branch := range res.Data {
		fmt.Println(branch.BranchCode, branch.BranchName, branch.SubBranchCode, branch.SubBranchName)
	}
}

func TestVats(t *testing.T) {
	assert := assert.New(t)

	sdk := NewSdk()

	res, err := sdk.Create(structs.CreateInputModel{
		OrgCode:      OrgCode,
		BranchNo:     BranchNo,
		DistrictCode: DistrictCode,
		Items:        items,
	})

	assert.Nil(err, fmt.Sprintf("Ebarimt Error : %v ", res.Message))

	if err != nil {
		return
	}

	// for _, receipt := range res.Receipts {
	// 	assert.Equal(utils.NumberPrecision(receipt.TotalAmount), receipt.TotalAmount, "TotalAmount Precision ")
	// 	assert.Equal(utils.NumberPrecision(receipt.TotalVat), receipt.TotalVat, "TotalVat Precision")
	// 	assert.Equal(utils.NumberPrecision(receipt.TotalCityTax), receipt.TotalCityTax, "TotalCityTax Precision")

	// 	for _, item := range receipt.Items {
	// 		assert.Equal(utils.NumberPrecision(item.TotalAmount), item.TotalAmount, "Receipt Item TotalAmount Precision")
	// 		assert.Equal(utils.NumberPrecision(item.TotalVat), item.TotalVat, "Receipt Item TotalVat Precision")
	// 		assert.Equal(utils.NumberPrecision(item.TotalCityTax), item.TotalCityTax, "Receipt Item TotalCityTax Precision")
	// 		assert.Equal(utils.NumberPrecision(item.UnitPrice), item.UnitPrice, "Receipt Item UnitPrice Precision")
	// 	}
	// }

	assert.Equal(constants.POS_STATUS_SUCCESS, res.Status, "Ebarimt Error : %v", res.Message)
}

func TestItems(t *testing.T) {
	assert := assert.New(t)

	assert.Equal(0.35, utils.NumberPrecision(0.35714285714285715), "Number Precision")
}

func TestSendMail(t *testing.T) {
	ebarimt3SdkServices.SendMail(
		ebarimt3SdkServices.EmailInput{
			Email:    "e.munkhsukh@gmail.com",
			From:     "noreply@dlife.mn",
			Password: "bUhxL1NRyXAt",
			User:     "noreply@dlife.mn",
			SmtpHost: "smtp.zoho.com",
			SmtpPort: "587",
			Response: structs.ReceiptResponse{
				QrData:       "428603689082365032246256586221443513359539633956812997336620885218633919593170065215693420197260872048943341453227343251885558988635319064587231649100449282811884172614913179956816409",
				ID:           "037900846788001095290000010012802",
				Date:         time.Now().Format("2006-01-02"),
				TotalAmount:  5000,
				TotalVat:     454.54,
				TotalCityTax: 0,
				Receipts: []structs.Receipt{
					{
						Items: []structs.Item{
							{
								Name:        "/4020 - SERVICE тоот/ -ын 1 сарын сунгалт",
								Qty:         1,
								TotalAmount: 5000,
							},
						},
					},
				},
			},
		},
	)
}
