package main
import("context";"encoding/json";"fmt";"io";"os";"strings";"time";"github.com/jackc/pgx/v5")
func main(){
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
 cfg,err:=pgx.ParseConfig(os.Getenv("DATABASE_URL"));if err!=nil{panic("invalid database config")}
 if cfg.Host!="127.0.0.1" && cfg.Host!="localhost"{panic("local database required")}
 if cfg.Database!="postgres" && !strings.HasPrefix(cfg.Database,"tes85_r7_") && !strings.HasPrefix(cfg.Database,"tes85_p0_r7_"){panic("disposable tes85_r7 database required")}
 c,err:=pgx.ConnectConfig(ctx,cfg);if err!=nil{panic("database connection failed")};defer c.Close(ctx)
 q,err:=io.ReadAll(os.Stdin);if err!=nil{panic(err)}
 if len(os.Args)>1 && os.Args[1]=="exec"{_,err=c.Exec(ctx,string(q));if err!=nil{panic(err)};fmt.Println("OK");return}
 rows,err:=c.Query(ctx,string(q));if err!=nil{panic(err)};defer rows.Close()
 values:=[][]any{};for rows.Next(){v,err:=rows.Values();if err!=nil{panic(err)};values=append(values,v)};if rows.Err()!=nil{panic(rows.Err())};json.NewEncoder(os.Stdout).Encode(values)
}
