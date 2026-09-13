// 数组排序
function arraySort(array, asc) {
    return array.sort(function (a, b) {
        return asc === 'asc' ? a - b : b - a;
    })
}

// 计算数组中每个成员出现的次数，返回一个去重的次数数组
function getCountArrayForGroupByCard(array, asc) {
    var ret = getGroupByCard(array);
    var r = [];
    for (var i in ret) {
        (r.indexOf(ret[i]) === -1) && r.push(ret[i]);
    }
    r = arraySort(r, asc);
    return r;
}

// 统计数组中每个成员出现的次数
function getGroupByCard(array) {
    var ret = {};
    array.forEach(function (item) {
        if (ret[item] === undefined) {
            ret[item] = 0;
        }
        ret[item]++;
    });
    return ret;
}

// 数组去重
function arrayClearRepeat(array) {
    var ret = [];
    array.forEach(function (item) {
        if (ret.indexOf(item) === -1) {
            ret.push(item);
        }
    });
    return ret;
}

// 从一个数组中过滤掉 >=n 的成员
function removeItemOverOf(array, n) {
    return array.filter(function (item) {
        return item < n;
    });
}

// 数组的最大成员是否 < n;
function maxItemLessThan(array, n) {
    return Math.max.apply(Math, array) < n;
}

// 数组的最大成员是否 >= n;
function maxItemMoreThan(array, n) {
    return Math.max.apply(Math, array) >= n;
}

// 获取数组中最小的成员;
function getMinItem(array) {
    if (!array.length) {
        return undefined;
    }
    return Math.min.apply(Math, array);
}
// 获取数组中最大的成员
function getMaxItem(array) {
    if (!array.length) {
        return undefined;
    }
    return Math.max.apply(Math, array);
}

// 筛选数组中累计出现过至少n次的成员
function getCardByCountOverOf(array, n) {
    var ret = getGroupByCard(array);
    var r = [];
    for (var i in ret) {
        if (ret[i] >= n) {
            r.push(parseInt(i));
        }
    }
    return r;
}

// 筛选数组中出现过n次的成员
function getCardByCount(array, n) {
    var ret = getGroupByCard(array);
    var r = [];
    for (var i in ret) {
        if (ret[i] === n) {
            r.push(parseInt(i));
        }
    }
    return r;
}

// 筛选数组中出现n次的成员与其它出现n次的成员，
// 若能组成等差数组，则返回这些成员的list（最长的那个等差数列,若长度一致，取最大的那一列）
function getSequence(array, n) {
    var r = arraySort(getCardByCount(array, n), 'asc');
    var rets = [];
    var ret = [];
    var maxIndex = r.length - 1;
    for (var i = 0; i < maxIndex; i++) {
        var prev = r[i];
        var curr = r[i + 1];
        if (curr - prev === 1 && curr < 15) {
            if (ret.indexOf(prev) === -1) {
                ret.push(prev);
            }
            if (ret.indexOf(curr) === -1) {
                ret.push(curr);
            }
            if (i === maxIndex - 1) {
                rets.push(ret);
            }
        } else {
            rets.push(ret);
            ret = [];
        }
    }
    rets = rets.sort(function (a, b) {
        return a.length - b.length;
    });
    return rets.pop() || [];

}

// 检查数组是否为等差数组 （差值 1）
function checkSequence(array) {
    array = arraySort(array, 'asc');
    for (var i = 0, len = array.length - 1; i < len; i++) {
        var prev = array[i];
        var current = array[i + 1];
        if (current - prev !== 1) {
            return false;
        }
    }
    return true;
}

// 出牌类型
const TYPES = {
    //  单张
    A: function (cards) {
        return {
            len: 1,
            key: cards[0],
            status: cards.length === 1
        }
    },
    // 对子
    AA: function (cards) {
        var status = cards.length === 2 && cards[0] === cards[1];
        return {
            len: 2,
            key: cards[0],
            status: status
        }
    },
    // 三张
    AAA: function (cards) {
        var status = cards.length === 3 && cards[0] === cards[1] && cards[1] === cards[2];
        return {
            len: 3,
            key: cards[0],
            status: status
        }
    },

    // 3小王炸
    XKING3: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 16 && cards.length === 3;
        return {
            len: 3,
            key: cards[0],
            status: status
        }
    },

    // 3大王炸
    DKING3: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 17 && cards.length === 3;
        return {
            len: 3,
            key: cards[0],
            status: status
        }
    },
    // 炸弹（四张）
    AAAA: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 4;
        return {
            len: 4,
            key: cards[0],
            status: status
        }
    },

    // 4小王炸
    XKING4: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 16 && cards.length === 4;
        return {
            len: 4,
            key: cards[0],
            status: status
        }
    },

    // 4大王炸
    DKING4: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 17 && cards.length === 4;
        return {
            len: 4,
            key: cards[0],
            status: status
        }
    },
    AAAA_5: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 5;
        return {
            len: 5,
            key: cards[0],
            status: status
        }
    },

    // 5小王炸
    XKING5: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 16 && cards.length === 5;
        return {
            len: 5,
            key: cards[0],
            status: status
        }
    },

    // 5大王炸
    DKING5: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 17 && cards.length === 5;
        return {
            len: 5,
            key: cards[0],
            status: status
        }
    },
    AAAA_6: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 6;
        return {
            len: 6,
            key: cards[0],
            status: status
        }
    },

    // 6小王炸
    XKING6: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 16 && cards.length === 6;
        return {
            len: 6,
            key: cards[0],
            status: status
        }
    },

    // 6大王炸
    DKING6: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && ret[0] === 17 && cards.length === 6;
        return {
            len: 6,
            key: cards[0],
            status: status
        }
    },
    AAAA_7: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 7;
        return {
            len: 7,
            key: cards[0],
            status: status
        }
    },
    AAAA_8: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 8;
        return {
            len: 8,
            key: cards[0],
            status: status
        }
    },
    AAAA_9: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 9;
        return {
            len: 9,
            key: cards[0],
            status: status
        }
    },
    AAAA_10: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 10;
        return {
            len: 10,
            key: cards[0],
            status: status
        }
    },
    AAAA_11: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 11;
        return {
            len: 11,
            key: cards[0],
            status: status
        }
    },
    AAAA_12: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 12;
        return {
            len: 12,
            key: cards[0],
            status: status
        }
    },
    AAAA_13: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 13;
        return {
            len: 13,
            key: cards[0],
            status: status
        }
    },
    AAAA_14: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 14;
        return {
            len: 14,
            key: cards[0],
            status: status
        }
    },
    AAAA_15: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 15;
        return {
            len: 15,
            key: cards[0],
            status: status
        }
    },
    AAAA_16: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 16;
        return {
            len: 16,
            key: cards[0],
            status: status
        }
    },
    AAAA_17: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 17;
        return {
            len: 17,
            key: cards[0],
            status: status
        }
    },
    AAAA_18: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 18;
        return {
            len: 18,
            key: cards[0],
            status: status
        }
    },
    AAAA_19: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 19;
        return {
            len: 19,
            key: cards[0],
            status: status
        }
    },
    AAAA_20: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 20;
        return {
            len: 20,
            key: cards[0],
            status: status
        }
    },
    AAAA_21: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 21;
        return {
            len: 21,
            key: cards[0],
            status: status
        }
    },
    AAAA_22: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 22;
        return {
            len: 22,
            key: cards[0],
            status: status
        }
    },
    AAAA_23: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 23;
        return {
            len: 23,
            key: cards[0],
            status: status
        }
    },
    AAAA_24: function (cards) {
        var ret = arrayClearRepeat(cards);
        var status = ret.length === 1 && cards.length === 24;
        return {
            len: 24,
            key: cards[0],
            status: status
        }
    },

}

var cardValidator = {
    1: [['A', TYPES.A]],
    2: [['AA', TYPES.AA]],
    3: [['AAA', TYPES.AAA], ['XKING3',TYPES.XKING3], ['DKING3',TYPES.DKING3]],
    4: [['AAAA', TYPES.AAAA], ['XKING4',TYPES.XKING4], ['DKING4',TYPES.DKING4]],
    5: [['AAAA_5', TYPES.AAAA_5], ['XKING5',TYPES.XKING5], ['DKING5',TYPES.DKING5]],
    6: [['AAAA_6', TYPES.AAAA_6], ['XKING6',TYPES.XKING6], ['DKING6',TYPES.DKING6]],
    7: [['AAAA_7',TYPES.AAAA_7]],
    8: [['AAAA_8',TYPES.AAAA_8]],
    9: [['AAAA_9',TYPES.AAAA_9]],
    10: [['AAAA_10',TYPES.AAAA_10]],
    11: [['AAAA_11',TYPES.AAAA_11]],
    12: [['AAAA_12',TYPES.AAAA_12]],
    13: [['AAAA_13',TYPES.AAAA_13]],
    14: [['AAAA_14',TYPES.AAAA_14]],
    15: [['AAAA_15',TYPES.AAAA_15]],
    16: [['AAAA_16',TYPES.AAAA_16]],
    17: [['AAAA_17',TYPES.AAAA_17]],
    18: [['AAAA_18',TYPES.AAAA_18]],
    19: [['AAAA_19',TYPES.AAAA_19]],
    20: [['AAAA_20',TYPES.AAAA_20]],
    21: [['AAAA_21',TYPES.AAAA_21]],
    22: [['AAAA_22',TYPES.AAAA_22]],
    23: [['AAAA_23',TYPES.AAAA_23]],
    24: [['AAAA_24',TYPES.AAAA_24]],
}


//验证牌型
function validate(cards) {
    var len = cards.length;
    var int_cards = cards.map(function (card) {
        return card.value;
    });
    var validators = cardValidator[len];
    if (len < 1 || len > 25 || !validators.length) {
        return {
            status: false,
            len: len,
            types: []
        };
    }
    var ret = [];
    validators.forEach(function (array) {
        var type = array[0];
        var validator = array[1];
        var result = validator(int_cards);
        if (result.status) {
            result.type = type;
            ret.push({key:result.key,type:type});
        }
    });
    if (ret.length) {
        return {
            status: true,
            len: len,
            types: ret
        }
    } else {
        return {
            status: false,
            len: len,
            types: []
        };
    }
}

// ==================== 出牌提示 ====================
// 与后端 internal/card/shape.go 对齐：牌型对象 {kind, rank, len}，
// kind 取值 single|pair|triple|bomb|kingbomb。改压牌规则时两边必须同步。

// 由 CTX_PLAY_CHANGE 的牌型描述（type/key/len）还原上家牌型；无有效描述返回 null
function parseShape(type, rank, len) {
    var n = len > 0 ? len : 0;
    if (!type || !n) {
        return null;
    }
    if (type === 'A') {
        return { kind: 'single', rank: rank, len: 1 };
    }
    if (type === 'AA') {
        return { kind: 'pair', rank: rank, len: 2 };
    }
    if (type === 'AAA') {
        return { kind: 'triple', rank: rank, len: 3 };
    }
    if (type.indexOf('AAAA') === 0) {
        return { kind: 'bomb', rank: rank, len: n };
    }
    if (type.indexOf('XKING') === 0) {
        return { kind: 'kingbomb', rank: 16, len: n };
    }
    if (type.indexOf('DKING') === 0) {
        return { kind: 'kingbomb', rank: 17, len: n };
    }
    return null;
}

// 候选牌型 c 能否压住桌面牌型 t（规则见 game-rules.md「压牌」与「大小王规则」）
function shapeBeats(c, t) {
    if (!c || !t) {
        return false;
    }
    var cKing = c.kind === 'kingbomb';
    var tKing = t.kind === 'kingbomb';
    if (cKing && tKing) {
        return c.len > t.len || (c.len === t.len && c.rank > t.rank);
    }
    if (cKing) {
        return t.len <= 2 * c.len - 1;
    }
    if (tKing) {
        return c.kind === 'bomb' && c.rank !== 16 && c.rank !== 17 && c.len > 2 * t.len - 1;
    }
    if (c.kind === 'bomb' && t.kind === 'bomb') {
        if (c.len === t.len) {
            return c.rank > t.rank;
        }
        return c.rank !== 16 && c.rank !== 17 && c.len > t.len;
    }
    if (c.kind === 'bomb') {
        return c.rank !== 16 && c.rank !== 17;
    }
    if (c.kind === t.kind && c.len === t.len) {
        return c.rank > t.rank;
    }
    return false;
}

// 一组同值牌取 l 张时的全部合法解读（与后端 Classify 一致：基础牌型在前，王炸在后）
function shapesOfPlay(value, l) {
    var shapes = [];
    if (l === 1) {
        shapes.push({ kind: 'single', rank: value, len: 1 });
    } else if (l === 2) {
        shapes.push({ kind: 'pair', rank: value, len: 2 });
    } else if (l === 3) {
        shapes.push({ kind: 'triple', rank: value, len: 3 });
    } else {
        shapes.push({ kind: 'bomb', rank: value, len: l });
    }
    if ((value === 16 || value === 17) && l >= 3 && l <= 6) {
        shapes.push({ kind: 'kingbomb', rank: value, len: l });
    }
    return shapes;
}

// 手牌候选：按「孤张单 → 对 → 三 → 炸弹」分组、组内牌值升序（与 e2e-audit 机器人一致）。
// 炸弹组例外：先按张数升序（4炸<5炸<6炸…），同张数再按牌面升序。
// 每个牌值按现有张数整体成组，不拆对/三：1=孤张单、2=对、3=三、
// ≥4 张或 3 张以上同王=炸弹。返回按组序排列的候选，每项为该组全部牌。
function hintCandidates(hand) {
    var groups = {};
    var order = [];
    hand.forEach(function (card) {
        if (!groups[card.value]) {
            groups[card.value] = [];
            order.push(card.value);
        }
        groups[card.value].push(card);
    });
    order.sort(function (a, b) {
        return a - b;
    });
    var buckets = [[], [], [], []]; // 单/对/三/炸弹
    order.forEach(function (value) {
        var cards = groups[value];
        var gi = cards.length - 1; // 1→单 2→对 3→三 ≥4→炸弹
        if ((value === 16 || value === 17) && cards.length >= 3) {
            gi = 3; // 3 张以上同王按王炸处理，不当作三条
        }
        if (gi > 3) {
            gi = 3;
        }
        buckets[gi].push(cards);
    });
    // 炸弹组：先按张数（4炸<5炸<6炸…），张数相同再按牌面从小到大
    buckets[3].sort(function (a, b) {
        if (a.length !== b.length) {
            return a.length - b.length;
        }
        return a[0].value - b[0].value;
    });
    return buckets[0].concat(buckets[1], buckets[2], buckets[3]);
}

// 在手牌中选出"刚好大过上家"的一组牌；仅做选择，不负责出牌。
// 组必须保持整体：存在对子/三条时不会拆成单张去跟牌，跟不住则返回 null（建议不出）。
// topShape 为 null 表示自由首出（一轮第一手）：按 孤张单→对→三→炸弹 取组序最前的一组。
function findHintCards(hand, topShape) {
    if (!hand || !hand.length) {
        return null;
    }
    var cands = hintCandidates(hand);

    // 自由首出：没有"上一手"可压，取组序最前的一组整体出
    if (!topShape) {
        return cands.length ? cands[0].slice() : null;
    }

    // 跟牌：按组序找第一组能压住的（组内牌值升序，即最"刚好"的一组）
    for (var i = 0; i < cands.length; i++) {
        var cards = cands[i];
        var shapes = shapesOfPlay(cards[0].value, cards.length);
        for (var j = 0; j < shapes.length; j++) {
            if (shapeBeats(shapes[j], topShape)) {
                return cards.slice();
            }
        }
    }
    return null;
}

