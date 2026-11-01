# Python

- PY-DEFAULTS: list/dict等の可変なデフォルト引数が呼び出し間で共有されることを確認する。インスタンスごとに独立が必要なdataclassの可変フィールドではdefault_factoryなどの生成方法を確認する。共有が仕様なら誤って独立化を要求しない。
- PY-ALIAS-STATE: 代入はオブジェクトをコピーせず、浅いコピーもネストした参照を共有する。失敗時の状態保持や入力非変更の契約がある場合、例外・早期returnまでの更新とfinallyの副作用を全状態について追う。
- PY-ABSENCE: None、空list/dict、0、Falseを同一の欠損と扱ってよいか契約を確認する。既定値を選ぶtruthiness判定が有効な入力を消していないか、例外の握りつぶしで失敗を成功として返していないかを調べる。

根拠:
- https://docs.python.org/3/tutorial/controlflow.html#default-argument-values
- https://docs.python.org/3/library/dataclasses.html#mutable-default-values
- https://docs.python.org/3/library/copy.html
- https://docs.python.org/3/library/stdtypes.html#truth-value-testing
