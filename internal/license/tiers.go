package license

// Seat tiers and list prices (kurus = TL * 100).
const (
	TierPersonal   = "personal"   // 1 user — ücretsiz bireysel
	TierSmall      = "small"      // 2–20
	TierMedium     = "medium"     // 21–100
	TierUnlimited  = "unlimited"  // 1000+ sınırsız
	TierUnlicensed = "unlicensed" // default free: 1 seat
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
		{Code: TierPersonal, Name: "1 Kullanıcı (Bireysel)", MaxUsers: 1, PriceTLYear: 0, PriceKurus: 0, Free: true, Description: "Tek kullanıcı — ücretsiz bireysel kullanım"},
		{Code: TierSmall, Name: "2–20 Kullanıcı", MaxUsers: 20, PriceTLYear: 499, PriceKurus: 49900, Description: "Küçük ekip"},
		{Code: TierMedium, Name: "21–100 Kullanıcı", MaxUsers: 100, PriceTLYear: 1499, PriceKurus: 149900, Description: "Orta ölçek"},
		{Code: TierUnlimited, Name: "1000+ Sınırsız", MaxUsers: 0, PriceTLYear: 2999, PriceKurus: 299900, Description: "Kurumsal / sınırsız kullanıcı"},
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

// UnlicensedMaxUsers: ücretsiz bireysel — yalnızca 1 kullanıcı.
const UnlicensedMaxUsers = 1
