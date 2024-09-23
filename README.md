#### Give a star before you see it. Ha ha ha ~ ~

Generates a protobuf file from your mysql database.

### Uses

##### Tips:  If your operating system is windows, the default encoding of windows command line is "GBK", you need to change it to "UTF-8", otherwise the generated file will be messed up. 



#### Use from the command line:

`go install github.com/yanshicheng/sql2pb@latest`

```
❯ sql2pb -h
Usage of sql2pb:
  -db string
        the database type (default "mysql")
  -field_style string
        gen protobuf field style, sql_pb | sqlPb (default "sqlPb")
  -go_package string
        the protocol buffer go_package. defaults to the database schema.
  -host string
        the database host (default "localhost")
  -ignore_columns string
        a comma spaced list of mysql columns to ignore
  -ignore_tables string
        a comma spaced list of tables to ignore
  -output_file string
        the output file path
  -package string
        the protocol buffer package. defaults to the database schema.
  -password string
        the database password
  -port int
        the database port (default 3306)
  -schema string
        the database schema
  -service_name string
        the protobuf service name , defaults to the database schema.
  -table string
        the table schema，multiple tables ',' split.  (default "*")
  -user string
        the database user (default "root")
```

```
$ sql2pb -go_package ./pb -host localhost -package pb -password root -port 3306 -schema usercenter -service_name usersrv -user root --output_file usersrv.proto
```



#### Use as an imported library

```sh
$ go get -u github.com/yanshicheng/sql2pb@latest
```



#### Thanks for Mikaelemmmm and schemabuf
    Mikaelemmmm  : github.com/Mikaelemmmm/sql2pb/core
    schemabuf : https://github.com/mcos/schemabuf
