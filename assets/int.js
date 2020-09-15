var ntimer, user, store = {}, sites = {}, nowedit, nowcmts, akey, ridlist = [],
    session = {};

function notice(t) { /*notice*/
    if (ntimer) {
        clearTimeout(ntimer);
    }
    $.ht(t, 'nt');
    SC('nt').style.opacity = 1;
    SC('nt').style.top = '0px';
    ntimer = setTimeout(function() {
        SC('nt').style.opacity = 0;
        SC('nt').style.top = '-80px';
    }, 2000);
} /*rouJS*/

function indexpage(print = true) {
    if (!store['index']) {
        $.aj('./x.php?a=getowned', '', {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    var data = j.data,
                        ht = '';
                    for (var i in data) {
                        var ob = data[i];
                        ht += '<div class=\'bk\'><h4 class=\'title\'>' + ob.name + '</h4><p class=\'desc\'>' + ob.intro + '</p><p class=\'ctrl\'><a href=\'#!edit/' + ob.id + '\'>管理</a><a href=\'#!cmts/' + ob.id + '\'>评论</a></p></div>';
                        sites[ob.id] = ob.name;
                    }
                    ht += '<div class=\'bk\'><a href=\'#!create\' class=\'cr\'>Create +</a></div>';
                    store['index'] = ht;
                    SC('bd').innerHTML = ht;
                } else if (j.msg == 'empty') {
                    notice('还没有任何评论框呢！');
                    SC('bd').innerHTML = '<div class=\'bk\'><a href=\'#!create\' class=\'cr\'>Create +</a></div>';
                } else {
                    notice('请求错误,msg:' + j.msg);
                }
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'get');
    } else {
        SC('bd').innerHTML = store['index'];
    }
}

function editpage(i) {
    return new Promise(function(res, rej) {
        $.aj('./x.php?a=getdetail', {
            id: i
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    var dms = j.data.domains;
                    var dmstr = '';
                    for (var k in dms) {
                        dmstr += dms[k] + ',';
                    }
                    j.data.domains = dmstr.slice(0, dmstr.length - 1); /*去除末尾逗号*/
                    res(j.data);
                } else {
                    notice(j.msg);
                    SC('bd').innerHTML = '<h2 class=\'crtitle\'>无权访问</h2>';
                }
            },
            failed: function() {
                notice('Network error.');
            }
        }, 'post');
    });
}
rou.x('bd').a('def', 'index', indexpage).a('reg', 'edit', function(k, i, pn) { /*注册编辑页*/
    var h = rqpage('edit');
    nowedit = pn[0];
    if (h instanceof Promise) { /*本地没有储存就直接promise*/
        h.then(function(d) {
            SC('bd').innerHTML = d;
            return editpage(pn[0]);
        }).then(function(m) {
            SC('etitle').innerHTML = m.name; /*get name*/
            SC('esite').value = m.name;
            SC('eintro').value = m.intro;
            SC('domains').value = m.domains;
        });
    } else {
        SC('bd').innerHTML = h;
        editpage(pn[0]).then(function(m) {
            SC('etitle').innerHTML = m.name; /*get name*/
            SC('esite').value = m.name;
            SC('eintro').value = m.intro;
            SC('domains').value = m.domains;
        })
    }
}).a('reg', 'create', function() {
    var h = rqpage('create'),
        ct;
    if (h instanceof Promise) {
        h.then(function(d) {
            SC('bd').innerHTML = d;
        });
    } else {
        SC('bd').innerHTML = h;
    }
}).a('reg', 'cmts', function(k, i, pn) { /*注册评论管理页*/
    var h = rqpage('cmts');
    akey = pn[1];
    nowcmts = pn[0];

    function setenter() {
        SC('esearch').onkeydown = function(ev) {
            if (ev && ev.keyCode == 13) {
                search();
            }
        }
    }
    if (h instanceof Promise) { /*本地没有储存就直接promise*/
        h.then(function(d) {
            SC('bd').innerHTML = d;
            setenter();
            listcomments(akey);
            return editpage(pn[0]);
        }).then(function(m) {
            SC('etitle').innerHTML = m.name; /*get name*/
        });
    } else {
        SC('bd').innerHTML = h;
        setenter();
        listcomments(akey);
        editpage(pn[0]).then(function(m) {
            SC('etitle').innerHTML = m.name; /*get name*/
        })
    }
}).r();

function rqpage(p) {
    if (p in store) {
        return store[p]; /*避免多次加载，存起来*/
    } else {
        return new Promise(function(res, rej) {
            $.aj('./assets/' + p + '.html', '', {
                success: function(m) {
                    store[p] = m;
                    res(m);
                },
                failed: function(m) {
                    notice('Network error.');
                }
            }, 'get');
        });
    }
}

function del() {
    if (confirm('真的要删除吗？！')) {
        $.aj('./x.php?a=del', {
            id: nowedit
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    notice('删除成功~');
                    delete store['index']; /*刷新缓存*/
                    window.open('#index', '_self');
                    //setTimeout(function(){location.reload()},3000);
                } else {
                    notice('删除失败,msg:' + j.msg);
                }
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'post');
    }
}

function topcm(e, md = false) { /*评论置顶(仅限主评论)*/
    if (!md) { /*调用选项*/
        callp({
            'opttitle': '请选择你要置顶的方式',
            'opt1': '仅当前对应页面',
            'opt2': '该评论框所有页面',
            'opt3': '手滑了'
        }, function(code) {
            switch (code) {
                case 1:
                    return topcm(e, 'single');
                case 2:
                    return topcm(e, 'all');
            }
        });
    } else {
        var rid = e.getAttribute('rid');
        $.aj('./x.php?a=top', {
            md: md,
            id: nowcmts,
            a: akey,
            rid: rid
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    notice('置顶成功~');
                    SC('topb').style.display = 'unset';
                } else {
                    notice('置顶失败,msg:' + j.msg);
                }
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'post');
    }
}

function notop(md = false) { /*去除评论置顶*/
    if (!md) {
        callp({
            'opttitle': '请选择你要取消的置顶',
            'opt1': '当前页面对应的置顶',
            'opt2': '评论框的全局置顶',
            'opt3': '手滑了'
        }, function(code) {
            switch (code) {
                case 1:
                    return notop('single');
                case 2:
                    return notop('all');
            }
        });
    } else {
        $.aj('./x.php?a=deltop', {
            md: md,
            id: nowcmts,
            a: akey
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    notice('移除置顶成功~');
                } else {
                    notice('移除置顶失败,msg:' + j.msg);
                }
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'post');
    }
}

function callp(show, cb) {
    for (var i in show) SC(i).innerHTML = show[i];
    SC('fopt').style.zIndex = 2;
    SC('fopt').style.opacity = 1;
    session['cb'] = function(code) {
        cb(code);
    }
}

function callbackp(code) {
    SC('fopt').style.zIndex = -1;
    SC('fopt').style.opacity = 0;
    session['cb'](code);
}

function delcm(e) { /*删除评论*/
    if (confirm('你真的要删除评论嘛')) {
        if (e instanceof Element) {
            var rid = e.getAttribute('rid'),
                md = 'single';
        } else if (e instanceof Array) {
            var rid = e,
                md = 'multi';
        }
        $.aj('./x.php?a=delcm', {
            md: md,
            id: nowcmts,
            a: akey,
            rid: rid
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code);
                if (code == 1) {
                    notice('删除成功~');
                } else {
                    notice('删除失败,msg:' + j.msg);
                }
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'post');
    }
}

function search(fid = false) {
    var words = SC('esearch').value;
    if (ce(words)) {
        $.aj('./x.php?a=search', {
            id: nowcmts,
            v: words
        }, {
            success: function(m) {
                var j = JSON.parse(m);
                var code = Number(j.code),
                    h = '',
                    d = j.data;
                if (code == 1) {
                    for (var i in d) h += '<p class=\'rs\'><a href=\'' + location.href + '/' + d[i].i + '\'>' + d[i].k + '</a></p>';
                    h = h == '' ? '<p class=\'rs\'>No result</p>' : h;
                    SC('result').innerHTML = h;
                } else {
                    notice('搜索失败,msg:' + j.msg);
                }
                SC('topb').style.display = j.alltop ? 'unset' : 'none';
            },
            failed: function(m) {
                notice('Network error.');
            }
        }, 'post');
    }
}

function linktoh(arr) {
    var p = '';
    for (var i in arr) {
        p += '<p class=\'rs-url\'><a href=\'' + arr[i] + '\' target=\'_blank\'>' + arr[i] + '</a></p>';
    }
    return p;
}

function checkbox() {
    var es = document.getElementsByClassName('rs-check');
    ridlist = [];
    for (var i in es) {
        if (es[i] instanceof Element) {
            var rid = es[i].getAttribute('rid');
            if (es[i].checked) ridlist.push(rid);
        }
    }
    SC('delb').style.display = ridlist.length > 0 ? 'unset' : 'none';
    SC('delb').onclick = function() {
        delcm(ridlist);
    }
}

function listcomments(akey, w) {
    if (ce(akey)) {
        SC('esearch').setAttribute('placeholder', '搜索评论...');
        SC('esearch').onkeydown = function(ev) {
            if (ev && ev.keyCode == 13) {
                clicke();
            }
        }
        SC('seab').removeAttribute('onclick');

        function clicke() {
            SC('seab').removeEventListener('click', clicke);
            var words = SC('esearch').value;
            listcomments(akey, words);
        }
        SC('seab').addEventListener('click', clicke);
        if (ce(w)) {
            $.aj('./x.php?a=scmt', {
                id: nowcmts,
                a: akey,
                w: w
            }, {
                success: function(m) {
                    var j = JSON.parse(m);
                    var code = Number(j.code),
                        h = '',
                        d = j.data,
                        construct = '';
                    if (code == 1) {
                        for (var i in d) {
                            var nd = d[i];
                            construct += '<h4 class=\'rs-title\'>' + nd.m.name + '<span style=\'color:#5882FA\'>[id]</span>' + nd.m.owner + '<span style=\'color:#5882FA\'>[date]</span>' + nd.m.date + '</h4><p class=\'rs-content\'>' + nd.m.content + '</p><div class=\'rs-links\'>' + linktoh(nd.m.pics) + '</div><a href=\'javascript:void(0);\' onclick=\'topcm(this)\' rid=\'' + nd.m.rid + '\'>置顶</a><a href=\'javascript:void(0);\' onclick=\'delcm(this)\' rid=\'' + nd.m.rid + '\'>删除</a><input type=\'checkbox\' class=\'rs-check\' rid=\'' + nd.m.rid + '\' onclick=\'checkbox()\'></input><ul>';
                            if (nd.r && nd.r.length > 0) {
                                var nnd = nd.r;
                                for (var i in nnd) construct += '<h4 class=\'rs-title\'>' + nnd[i].m.name + '<span style=\'color:#5882FA\'>[id]</span>' + nnd[i].m.owner + '<span style=\'color:#5882FA\'>[date]</span>' + nnd[i].m.date + '</h4><p class=\'rs-content\'>' + nnd[i].m.content + '</p><div class=\'rs-links\'>' + linktoh(nnd[i].m.pics) + '</div><a href=\'javascript:void(0);\' onclick=\'delcm(this)\' rid=\'' + nnd[i].m.rid + '\'>删除</a><input type=\'checkbox\' class=\'rs-check\' rid=\'' + nnd[i].m.rid + '\' onclick=\'checkbox()\'></input>';
                            }
                            construct += '</ul>';
                        }
                        construct = construct == '' ? '<p class=\'rs\'>No result</p>' : construct;
                        SC('result').innerHTML = construct;
                        checkbox();
                    } else {
                        notice('搜索失败,msg:' + j.msg);
                    }
                    SC('topb').style.display = j.top ? 'unset' : 'none';
                },
                failed: function(m) {
                    notice('Network error.');
                }
            }, 'post');
        }
    }
}

function edit() {
    var site = SC('esite').value,
        intro = SC('eintro').value,
        dms = SC('domains').value;
    if (!ce(site) || !ce(intro) || !ce(dms)) {
        notice('不能留空~');
        return false;
    }
    $.aj('./x.php?a=edit', {
        id: nowedit,
        st: site,
        it: intro,
        ds: dms
    }, {
        success: function(m) {
            var j = JSON.parse(m);
            var code = Number(j.code);
            if (code == 1) {
                notice('编辑成功~');
                window.open('#index', '_self');
            } else {
                notice('编辑失败,msg:' + j.msg);
            }
        },
        failed: function(m) {
            notice('Network error.');
        }
    }, 'post');
}

function ce(v) {
    if (v == null || String(v) == 'undefined' || v.match(/^\s*$/)) return false
    else return true;
}

function create() {
    var site = SC('site').value,
        intro = SC('intro').value;
    if (!ce(site) || !ce(intro)) {
        notice('不能留空~');
        return false;
    }
    $.aj('./x.php?a=create', {
        st: site,
        it: intro
    }, {
        success: function(m) {
            var j = JSON.parse(m);
            var code = Number(j.code);
            if (code == 1) {
                notice('创建成功~');
                delete store['index']; /*刷新缓存*/
                window.open('#index', '_self');
                //setTimeout(function(){location.reload()},3000);
            } else {
                notice('创建失败,msg:' + j.msg);
            }
        },
        failed: function(m) {
            notice('Network error.');
        }
    }, 'post');
}

function account() {
    if (confirm('是否切换账号？')) {
        window.open('./u.php?q=destroy', '_self');
    }
} /*requestdetail*/
$.aj('./u.php', '', {
    success: function(m) {
        var j = JSON.parse(m);
        code = Number(j['code']);
        if (code == 1) {
            user = j['name'];
            SC('wl').innerHTML = SC('wl').innerHTML.replace('[username]', user);
        } else if (code == 0) {
            notice('尚未登录');
            SC('wl').innerHTML = SC('wl').innerHTML.replace('[username]', 'Stranger');
            SC('sw').innerHTML = 'Login';
            SC('sw').onclick = '';
            SC('sw').href = './oa';
        } else {
            notice('获取信息失败');
        }
    },
    failed: function(m) {
        notice('Network error.');
    }
}, 'get');