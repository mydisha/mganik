// Package iamtest hanya berisi test integrasi IAM (login, sesi, resolver,
// CSRF, OTP konsumen) terhadap Postgres sungguhan dan router.New. Dipisah dari
// package service/router supaya TestMain-nya (testutil.Main) tidak bentrok
// dengan test stream lain yang menambah TestMain di package tersebut.
package iamtest
