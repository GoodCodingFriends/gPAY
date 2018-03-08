package controller

var (
	errToSlackMessage = map[error]string{
		ErrUnknownCommand: "わからないコマンドだよ",
		ErrInvalidUsage:   "多分変な使い方してる",
		ErrInvalidUserID:  "@ をつけてもう一度お願い",
	}
	balanceMessage      = "今の残高は %d 円で、あと %d 円分つかえると思う"
	balanceLimitMessage = "今の残高は %d 円だからもう使えないよ。お金返してあげなよ"
	claimMessage        = "<@%s> から %d 円分を請求されたよ"
)
