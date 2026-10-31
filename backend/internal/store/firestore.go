package store

import (
	"context"
	"time"

	"ai-factory/backend/internal/domain"
	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Store struct{ DB *firestore.Client }

func (s *Store) ref(uid, id string) *firestore.DocumentRef {
	return s.DB.Collection("users").Doc(uid).Collection("sessions").Doc(id)
}
func (s *Store) Get(ctx context.Context, uid, id string) (*domain.Session, error) {
	d, err := s.ref(uid, id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var v domain.Session
	if err = d.DataTo(&v); err != nil {
		return nil, err
	}
	v.Normalize()
	if time.Now().After(v.ExpiresAt) {
		return nil, domain.ErrNotFound
	}
	return &v, nil
}
func (s *Store) Create(ctx context.Context, uid string, v *domain.Session) error {
	_, err := s.ref(uid, v.ID).Create(ctx, v)
	return err
}
func (s *Store) List(ctx context.Context, uid string) ([]domain.Session, error) {
	docs, err := s.DB.Collection("users").Doc(uid).Collection("sessions").OrderBy("UpdatedAt", firestore.Desc).Limit(40).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := []domain.Session{}
	for _, d := range docs {
		var v domain.Session
		if err := d.DataTo(&v); err != nil {
			return nil, err
		}
		if time.Now().Before(v.ExpiresAt) {
			v.Messages = []domain.Message{}
			v.Summary = ""
			v.Review = nil
			v.Implementation = nil
			out = append(out, v)
		}
	}
	return out, nil
}

// One renewable lease per user also fences all session writes across Cloud Run instances.
func (s *Store) Acquire(ctx context.Context, uid, id, lease string) (*domain.Session, error) {
	var v domain.Session
	lock := s.DB.Collection("users").Doc(uid).Collection("locks").Doc("connection")
	err := s.DB.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, err := tx.Get(s.ref(uid, id))
		if status.Code(err) == codes.NotFound {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err = d.DataTo(&v); err != nil {
			return err
		}
		v.Normalize()
		if time.Now().After(v.ExpiresAt) {
			return domain.ErrNotFound
		}
		l, err := tx.Get(lock)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err == nil {
			var prev struct{ Until time.Time }
			if err = l.DataTo(&prev); err != nil {
				return err
			}
			if prev.Until.After(time.Now()) {
				return domain.ErrConflict
			}
		}
		if v.LeaseUntil.After(time.Now()) {
			return domain.ErrConflict
		}
		v.Lease = lease
		v.LeaseUntil = time.Now().Add(45 * time.Second)
		if v.State == "running" || v.State == "accepted" || v.State == "stopping" {
			v.State = "auth_wait"
		}
		if err = tx.Set(lock, map[string]any{"Lease": lease, "Until": v.LeaseUntil}); err != nil {
			return err
		}
		return tx.Set(s.ref(uid, id), v)
	})
	return &v, err
}
func (s *Store) Save(ctx context.Context, uid string, v *domain.Session) error {
	lock := s.DB.Collection("users").Doc(uid).Collection("locks").Doc("connection")
	return s.DB.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, err := tx.Get(s.ref(uid, v.ID))
		if err != nil {
			return err
		}
		var prev domain.Session
		if err = d.DataTo(&prev); err != nil {
			return err
		}
		l, err := tx.Get(lock)
		if err != nil {
			return err
		}
		var owner struct {
			Lease string
			Until time.Time
		}
		if err = l.DataTo(&owner); err != nil {
			return err
		}
		if prev.Lease != v.Lease || owner.Lease != v.Lease || !owner.Until.After(time.Now()) {
			return domain.ErrConflict
		}
		v.UpdatedAt = time.Now().UTC()
		v.LeaseUntil = time.Now().Add(45 * time.Second)
		if err = tx.Set(lock, map[string]any{"Lease": v.Lease, "Until": v.LeaseUntil}); err != nil {
			return err
		}
		return tx.Set(s.ref(uid, v.ID), v)
	})
}
func (s *Store) Release(ctx context.Context, uid string, v *domain.Session) error {
	lock := s.DB.Collection("users").Doc(uid).Collection("locks").Doc("connection")
	return s.DB.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, err := tx.Get(s.ref(uid, v.ID))
		if err != nil {
			return err
		}
		var prev domain.Session
		if err = d.DataTo(&prev); err != nil {
			return err
		}
		l, err := tx.Get(lock)
		if err != nil {
			return err
		}
		var owner struct{ Lease string }
		if err = l.DataTo(&owner); err != nil {
			return err
		}
		if prev.Lease != v.Lease || owner.Lease != v.Lease {
			return domain.ErrConflict
		}
		v.Lease = ""
		v.LeaseUntil = time.Time{}
		v.UpdatedAt = time.Now().UTC()
		if err = tx.Delete(lock); err != nil {
			return err
		}
		return tx.Set(s.ref(uid, v.ID), v)
	})
}
func (s *Store) Delete(ctx context.Context, uid, id string) error {
	return s.DB.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, err := tx.Get(s.ref(uid, id))
		if status.Code(err) == codes.NotFound {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		var v domain.Session
		if err = d.DataTo(&v); err != nil {
			return err
		}
		if v.LeaseUntil.After(time.Now()) {
			return domain.ErrConflict
		}
		return tx.Delete(s.ref(uid, id))
	})
}
