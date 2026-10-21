"""示例脚本：仅用于验证「scripts/ 只展示、不执行」。

服务端不会运行本文件，也不会把它读进对话上下文。
"""


def is_right_triangle(a: float, b: float, c: float) -> bool:
    """判断三边能否构成直角三角形（c 为最长边）。"""
    sides = sorted([a, b, c])
    return abs(sides[0] ** 2 + sides[1] ** 2 - sides[2] ** 2) < 1e-9
