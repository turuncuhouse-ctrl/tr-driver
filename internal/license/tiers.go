package license

// Seat tiers and list prices (kurus = TL * 100). Editable via env later.
const (
	TierPersonal   = "personal"   // 1 user
	TierSmall      = "small"      // up to 10
	TierMedium     = "medium"     // up to 50
	TierUnlimited  = "unlimited"  // 50+
	TierUnlicensed = "unlicensed" // default: 1 seat
)

type TierInfo struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	MaxUsers     int    `json:"maxUsers"` // 0 = unlimited
	PriceTLYear  int    `json:"priceTlYear"`
	PriceKurus   int64  `json:"priceKurus"`
	Description  string `json:"description"`
}

func Catalog() []TierInfo {
	return []TierInfo{
		{Code: TierPersonal, Name: "1 Kullanıcı", MaxUsers: 1, PriceTLYear: 99, PriceKurus: 9900, Description: "Tek kullanıcılı kişisel kurulum"},
		{Code: TierSmall, Name: "1–10 Kullanıcı", MaxUsers: 10, PriceTLYear: 499, PriceKurus: 49900, Description: "Küçük ekip"},
		{Code: TierMedium, Name: "11–50 Kullanıcı", MaxUsers: 50, PriceTLYear: 1499, PriceKurus: 149900, Description: "Orta ölçek"},
		{Code: TierUnlimited, Name: "50+ Sınırsız", MaxUsers: 0, PriceTLYear: 2999, PriceKurus: 299900, Description: "Kurumsal / sınırsız kullanıcı"},
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

// UnlicensedMaxUsers is the free self-host seat limit until a key is activated.
const UnlicensedMaxUsers = 1
