package license

// Seat tiers — TR Driver is free for all seats (no paid packaging).
const (
	TierPersonal   = "personal"
	TierSmall      = "small"
	TierMedium     = "medium"
	TierUnlimited  = "unlimited"
	TierUnlicensed = "unlicensed" // default: unlimited free seats
)

type TierInfo struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	MaxUsers    int    `json:"maxUsers"` // 0 = unlimited
	PriceTLYear int    `json:"priceTlYear"`
	PriceKurus  int64  `json:"priceKurus"`
	Description string `json:"description"`
	Free        bool   `json:"free"`
}

func Catalog() []TierInfo {
	return []TierInfo{
		{Code: TierPersonal, Name: "Bireysel", MaxUsers: 0, PriceTLYear: 0, PriceKurus: 0, Free: true, Description: "Tamamen ücretsiz"},
		{Code: TierSmall, Name: "Ekip", MaxUsers: 0, PriceTLYear: 0, PriceKurus: 0, Free: true, Description: "Tamamen ücretsiz — kullanıcı limiti yok"},
		{Code: TierMedium, Name: "Kurum", MaxUsers: 0, PriceTLYear: 0, PriceKurus: 0, Free: true, Description: "Tamamen ücretsiz — kullanıcı limiti yok"},
		{Code: TierUnlimited, Name: "Sınırsız", MaxUsers: 0, PriceTLYear: 0, PriceKurus: 0, Free: true, Description: "Tamamen ücretsiz"},
	}
}

func MaxUsersForTier(code string) (int, bool) {
	for _, t := range Catalog() {
		if t.Code == code {
			return t.MaxUsers, true
		}
	}
	return 0, false
}

// UnlicensedMaxUsers: 0 = unlimited free seats (no license purchase required).
const UnlicensedMaxUsers = 0
