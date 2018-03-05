package usecase

import (
	"context"

	"github.com/GoodCodingFriends/gpay/entity"
	"github.com/GoodCodingFriends/gpay/repository"
)

type PayParam struct {
	From, To *entity.User
	Amount   entity.Amount
	Message  string
}

func Pay(repo *repository.Repository, p *PayParam) (*entity.Transaction, error) {
	result, err := p.From.Pay(p.To, p.Amount, p.Message)
	if err != nil {
		return nil, err
	}

	tx, ctx, err := repo.BeginTx(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := recover(); err != nil {
			tx.Rollback()
			panic(err)
		} else if err != nil {
			tx.Rollback()
		}
	}()

	err = tx.User.StoreAll(ctx, []*entity.User{p.From, p.To})
	if err != nil {
		return nil, err
	}

	err = tx.Transaction.Store(ctx, result)
	if err != nil {
		return nil, err
	}

	return result, tx.Commit()
}
