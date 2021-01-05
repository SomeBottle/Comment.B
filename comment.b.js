/*Comment.B Beta1.6*/
var CB = {
    mainpath: './',
    gravatar: 'https://cn.gravatar.com/avatar/',
    ini: function() { /*initialization，必要函数初始化*/
        String.prototype.rpl = function(origin, to) { /*在String类型原型链上加一个简化replace的函数方法*/
            /*ExpReg需要转义一道，正则匹配还需要转义一道，就形成了反斜杠超级加倍*/
            return this.replace(new RegExp('\\{\\[' + origin + '\\]\\}', 'gi'), to);
        }
        String.prototype.extract = function(between) { /*在String类型原型链上加一个提取特定内容的函数方法*/
            var ad = this.split(new RegExp('\\{\\{' + between + '\\}\\}', 'i'))[1];
            return ad.split(new RegExp('\\{\\{' + between + 'end\\}\\}', 'i'))[0];
        }
    },
    const: function() { /*识别并构造评论框，梦开始的地方*/
        var o = this;
        if (o.tploaded !== '') { /*哥们儿，模板加载了没？*/
            var all = document.getElementsByClassName('cm-b');
            for (var i in all) {
                if (all[i] instanceof Element) { /*兄die，你这要是元素才行啊*/
                    var akey = all[i].getAttribute('akey'),
                        aid = akey + o.id;
                    if (!akey) break; /*如果不存在akey就给他一巴掌扇回去*/
                    all[i].removeAttribute('akey'); /*过河拆akey，防止重复const.老铁们，我做的对吗？*/
                    o.temparse(aid, akey); /*重组模板*/
                    (function(e, key, aid) {
                        o.ht('<p style=\"line-height: 50px;text-align:center;color:#AAA;\">Comment.B Loading</p>', e);
                        o.rq(o.mainpath + 'u.php', {}, {
                            success: function(m) {
                                var j = JSON.parse(m),
                                    code = Number(j['code']),
                                    tp = '';
                                if (code == 1) {
                                    user = j['name'];
                                    tp = o.tpgetter('login', aid).rpl('user', user);
                                } else if (code == 0) {
                                    user = '';
                                    tp = o.tpgetter('notlogin', aid);
                                } else {
                                    alert('评论框初始化失败：500');
                                } /*验证评论框是否能在该页面展开*/
                                o.verify().then(function(m) {
                                    return new Promise(function(res, rej) {
                                        if (m.code == 1) {
                                            o.framesid[key] = m.frid; /*储存评论框id*/
                                            o.framesown[aid] = {
                                                'if': m.ifown,
                                                'user': user,
                                                'ad': m.ad
                                            }; /*储存评论框是否为用户所掌控*/
                                            o.rq(o.mainpath + 'c.php?a=gtcm', { /*获取评论内容和评论数*/
                                                k: key,
                                                id: m.frid
                                            }, {
                                                success: function(m) {
                                                    var j = JSON.parse(m);
                                                    res(j);
                                                },
                                                failed: function(m) {
                                                    res({
                                                        code: 0
                                                    });
                                                }
                                            }, 'post');
                                        } else {
                                            res({
                                                code: 0
                                            });
                                        }
                                    });
                                }).then(function(m) {
                                    if (m.code == 1) {
                                        var ts = m.tops,
                                            atopcmlist = o.floorrender(ts.a, aid, 'commenttopitem'),
                                            gtopcmlist = o.floorrender(ts.g, aid, 'commentglobaltopitem', ts.gea),
                                            topcmlist = gtopcmlist + atopcmlist; /*渲染置顶*/
                                        o.atoprid[aid] = ts.a ? ts.a[0].m.rid : false;
                                        o.gtoprid[aid] = ts.g ? ts.g[0].m.rid : false;
                                        var cmlist = o.floorrender(m.data, aid); /*渲染置顶后再渲染普通评论，顺序不可颠倒*/
                                        o.mainparts[aid] = m.part; /*记录主评论切割的长度*/
                                        tp = m.cut ? tp.rpl('CommentBBottom', o.tpmd[aid]['bottommore']) : tp.rpl('CommentBBottom', o.tpmd[aid]['bottom']); /*切割了的话就加载更多*/
                                        tp = tp.rpl('CommentsNum', m.cmnum); /*更新评论数*/
                                        tp = tp.rpl('Comments', topcmlist + cmlist); /*渲染评论*/
                                        cmlist = topcmlist = null;
                                    } else {
                                        tp = o.tpgetter('notconfig', aid);
                                    }
                                    o.ht(tp, e);
                                });
                            },
                            failed: function(m) {
                                o.ht('', e);
                                alert('评论框初始化失败：服务器连接失败');
                            }
                        }, 'get');
                    })(all[i], akey, aid); /*闭包传参，可以解决在for,while等循环中的变量恒定问题*/
                    o.id += 1;
                }
            }
        } else {
            o.rq(o.mainpath + 'c.php?a=tp', {}, {
                success: function(m) {
                    o.tploaded = m; /*获得到模板文件*/
                    localStorage.commentbtp = m; /*缓存模板到本地*/
                    return o.const(); /*重返构建*/
                },
                failed: function(m) {
                    if (!localStorage.commentbtp) {
                        o.ht('', e);
                        alert('评论框初始化失败：模板无法获取');
                    }
                }
            }, 'get');
            if (localStorage.commentbtp) {
                o.tploaded = localStorage.commentbtp;
                return o.const(); /*重返构建*/
            }
        }
    },
    temparse: function(aid, akey) { /*模板一次处理*/
        var o = this,
            temp = o.tploaded.rpl('aid', aid).rpl('akey', akey).rpl('mainpath', o.mainpath).rpl('nowpath', window.location.href); /*加上特殊标识AID，值得注意的是这个不是akey文章标识符*/
        o.tpparser(temp, aid); /*拆分处理模板*/
    },
    replyrender: function(sm, ct, aid) { /*回复楼层渲染器*/
        var o = this,
            subtp = '';
        for (var it in sm) {
            subtp += o.csic(sm[it]['m'], aid);
            var mrid = ct ? ct['m']['rid'] : false,
                rpart = ct ? ct['rpart'] : false; /*获得主评论rid*/
            o.cmindex[sm[it].m.rid] = sm[it]['m']['name']; /*存入rid索引用户名*/
        }
        return {
            subtp: subtp,
            mrid: mrid,
            rpart: rpart
        };
    },
    floorrender: function(data, aid, type = 'commentitem', renderbtn = true) { /*主楼层渲染器(数组,aid,渲染模板类型,是否渲染小按钮例如删除、回复)*/
        var o = this,
            cmlist = '';
        for (var cm in data) {
            var ct = data[cm],
                rid = ct.m.rid;
            if (rid !== o.atoprid[aid]) {
                var cmtp = o.cic(ct['m'], aid, type, renderbtn),
                    sm = ct['r'] || {}; /*嵌套拉取回复层*/
                o.cmindex[rid] = ct['m']['name']; /*存入rid索引用户名*/
                var rprd = o.replyrender(sm, ct, aid),
                    subtp = rprd.subtp,
                    mrid = rprd.mrid,
                    rpart = rprd.rpart;
                cmtp = ct.rcut ? cmtp.rpl('MoreSubBtn', o.tpmd[aid]['moresubbtn']).rpl('mrid', mrid).rpl('rpart', rpart) : cmtp.rpl('MoreSubBtn', '');
                cmtp = cmtp.rpl('ReplyContent', subtp); /*将回复层嵌套上去*/
                cmlist += cmtp; /*集合所有评论*/
            }
        }
        return cmlist;
    },
    ce: function(v) { /*判断是否输入内容，老实说我自己都有点模糊这个是怎么用的了*/
        if (v == null || String(v) == 'undefined' || v.match(/^\s*$/)) return false
        else return true;
    },
    tpparser: function(tp, aid) { /*评论框模板解释器，模板二次处理*/
        var o = this,
            temps = ['main', 'nologintop', 'logintop', 'commentitem', 'commenttopitem', 'commentglobaltopitem', 'commentpicscon', 'commentpics', 'bottom', 'bottommore', 'noconfigbottom', 'replybtn', 'deletebtn', 'moresubbtn', 'commentreplyitem', 'commentreplypics', 'commentreplypicscon'];
        o.tpmd = o.tpmd || {};
        o.tpmd[aid] = new Object();
        for (var i in temps) {
            var t = temps[i];
            o.tpmd[aid][t] = tp.extract(t, t + 'end'); /*逐个提取模板内容*/
        }
    },
    renderurl: function(c) { /*渲染评论中的url*/
        var rex = new RegExp('((https|http|ftp|rtsp|mms)?:\\/\\/)[^\\s]+', 'gi');
        c = c.replace(rex, function(url) {
            return '<a href="' + url + '" target="_blank">' + url + '</a>';
        }); /*replace的奇妙用法，后面可以用函数回调，老铁们，你们学到了吗*/
        return c;
    },
    tpgetter: function(type, aid) { /*模板组装器*/
        var o = this;
        if (type == 'notlogin') {
            return o.tpmd[aid]['main'].rpl('CommentBTop', o.tpmd[aid]['nologintop']);
        } else if (type == 'login') {
            return o.tpmd[aid]['main'].rpl('CommentBTop', o.tpmd[aid]['logintop']);
        } else if (type == 'notconfig') {
            return o.tpmd[aid]['main'].rpl('CommentBTop', '').rpl('CommentBBottom', o.tpmd[aid]['noconfigbottom']).rpl('Comments', '');
        }
    },
    verify: function() { /*验证评论框是否能在该页面展开*/
        var host = window.location.host,
            o = this;
        return new Promise(function(res, rej) {
            o.rq(o.mainpath + 'c.php?a=verify', {
                dm: host
            }, {
                success: function(m) {
                    var j = JSON.parse(m);
                    res(j);
                },
                failed: function(m) {
                    res({
                        code: 0
                    });
                }
            }, 'post');
        });
    },
    rq: function(p, d, sf, m) { /*(path,data,success or fail,method,cookie)*/
        var xhr = new XMLHttpRequest();
        xhr.withCredentials = true; /*解决跨域无cookie导致无法登录的问题，这里在后端真的是很要加点东西，不得不说Chrome的跨域安全真的做的顶呱呱*/
        var hm = '';
        for (var ap in d) {
            hm = hm + ap + '=' + d[ap] + '&';
        }
        hm = hm.substring(0, hm.length - 1);
        if (m !== 'multipart/form-data') {
            xhr.open(m, p, true);
            xhr.setRequestHeader("Content-type", "application/x-www-form-urlencoded");
            xhr.send(hm);
        } else {
            xhr.open('post', p, true);
            xhr.send(d);
        }
        xhr.onreadystatechange = function() {
            if (xhr.readyState == 4 && xhr.status == 200) {
                sf.success(xhr.responseText);
            } else if (xhr.readyState == 4 && xhr.status !== 200) {
                sf.failed(xhr.status);
            }
        };
    },
    tploaded: '',
    ls: new Array(),
    lss: '',
    sendqueue: {},
    /*记录框架发送评论的队列，防止一次多发*/
    atoprid: {},
    /*记录局部置顶评论的rid，用来避免渲染列表时再次出现*/
    gtoprid: {},
    /*记录全局置顶评论的rid，用来避免渲染列表时再次出现*/
    framesid: {},
    /*用于索引frameid*/
    framesown: {},
    /*用于记录每个评论框对应的是否主人，是否可删评，用户名*/
    mainparts: {},
    /*储存主评论下加载更多的进程*/
    replylist: {},
    /*储存每个评论框申请的reply*/
    cmindex: {},
    /*以评论RID索引对应用户name，用于搭配reply*/
    id: 0,
    /*评论框ID*/
    script: function(url) { /*往dom中增加script*/
        var script = document.createElement("script"),
            exist = false,
            o = this;
        for (var up in o.ls) {
            if (o.ls[up] == url) {
                exist = true;
                break;
            }
        }
        if (!exist) {
            o.ls[o.ls.length] = url;
            script.type = "text/javascript";
            script.src = url;
            document.body.appendChild(script);
        }
    },
    s: function(id) { /*简单根据ID获取元素，当我写完comment.b时突然想起来，不是有querySelector嘛！*/
        return document.getElementById(id);
    },
    ht: function(h, e) { /*设置innerHTML的函数，对js执行有处理*/
        if (e instanceof Element) {
            ht = e;
            ht.innerHTML = h;
            os = ht.getElementsByTagName('script');
            var scr = '',
                ot = this;
            for (var o = 0; o < os.length; o++) {
                scr = scr + os[o].innerHTML;
                if (os[o].src !== undefined && os[o].src !== null && os[o].src !== '') {
                    ot.script(os[o].src);
                } else {
                    if (ot.lss !== scr) {
                        setTimeout(os[o].innerHTML, 0); /*用setTimeout替换eval*/
                    }
                }
            }
        }
    },
    cic: function(arr, aid, type = 'commentitem', renderbtn = true) { /*CommentItemConstructor主评论构筑器(数组,aid,渲染模板类型,是否渲染小按钮例如删除、回复)*/
        var o = this,
            x = CB.framesown[aid],
            tp = o.tpmd[aid][type],
            rptp = renderbtn ? o.tpmd[aid]['replybtn'] : '',
            deltp = (x.
            if ||(x.user == arr.name && x.ad)) ? (renderbtn ? o.tpmd[aid]['deletebtn'] : '') : '',
            picstp = o.tpmd[aid]['commentpics'],
            pics = arr.pics,
            pc = '',
            blog = arr.blog == '' ? '' : arr.blog;
        if (pics.length > 0) { /*装填图片*/
            for (var i in pics) pc += picstp.rpl('ImgUrl', pics[i]);
        }
        pc = pc == '' ? pc : o.tpmd[aid]['commentpicscon'].rpl('Pics', pc);
        return tp.rpl('Avatar', o.gravatar + arr.email).rpl('UserName', arr.name).rpl('Content', o.renderurl(arr.content)).rpl('Date', arr.date).rpl('ReplyBtn', rptp).rpl('DeleteBtn', deltp).rpl('rid', arr.rid).rpl('CommentPics', pc).rpl('Blog', blog);
    },
    csic: function(arr, aid) { /*CommentSubItemConstructor子评论构筑器*/
        var o = this,
            tp = o.tpmd[aid]['commentreplyitem'],
            x = CB.framesown[aid],
            rptp = o.tpmd[aid]['replybtn'],
            deltp = (x.
            if ||(x.user == arr.name && x.ad)) ? o.tpmd[aid]['deletebtn'] : '',
            rpname = arr.rpnm == '' ? '' : ' > ' + arr.rpnm,
            /*如果回复的不是子评论，子评论就不以xxxx > xxx的形式显示*/
            picstp = o.tpmd[aid]['commentreplypics'],
            pics = arr.pics,
            pc = '',
            blog = arr.blog == '' ? '' : arr.blog;
        if (pics.length > 0) { /*装填图片*/
            for (var i in pics) pc += picstp.rpl('ImgUrl', pics[i]);
        }
        pc = pc == '' ? pc : o.tpmd[aid]['commentreplypicscon'].rpl('Pics', pc);
        return tp.rpl('Avatar', o.gravatar + arr.email).rpl('UserName', arr.name + rpname).rpl('Content', o.renderurl(arr.content)).rpl('Date', arr.date).rpl('ReplyBtn', rptp).rpl('DeleteBtn', deltp).rpl('rid', arr.rid).rpl('CommentPics', pc).rpl('Blog', blog);
    },
    rp: function(rid, aid, container, cb) { /*回复处理函数(rid，aid，回复操作容器,callback)*/
        var o = this;
        if (o.framesown[aid].user == '') {
            alert('请登录后再回复');
            return false;
        }
        o.replylist[aid] = {
            rid: rid,
            con: container
        };
        cb(o.cmindex[rid]); /*callback(name)*/
    },
    del: function(rid, akey, cb) { /*删除处理函数(rid，akey，callback)*/
        var o = this,
            frameid = o.framesid[akey];
        o.rq(o.mainpath + 'c.php?a=dl', {
            a: akey,
            id: frameid,
            rid: rid
        }, {
            success: function(m) {
                j = JSON.parse(m);
                if (j.code == 1) {
                    cb();
                } else {
                    alert(j.msg || '评论删除失败');
                }
            },
            failed: function(m) {
                alert('评论删除失败');
            }
        }, 'post');
    },
    crp: function(aid, cb) { /*取消回复处理函数(aid,callback)*/
        delete this.replylist[aid];
        cb();
    },
    sm: function(akey, aid, el, ct, cb) { /*提交用函数(akey，aid, 评论部分容器元素ID，提交内容,callback(返回数据,mrid))*/
        var o = this;
        o.replylist[aid] = o.replylist[aid] || {};
        if (!o.sendqueue[aid]) { /*计入队列，防止撞车*/
            var frameid = o.framesid[akey],
                replyto = o.replylist[aid]['rid'] == 0 ? 0 : (o.replylist[aid]['rid'] || false);
            o.sendqueue[aid] = true;
            if (!o.ce(ct)) {
                alert('不要说空气哦');
                o.sendqueue[aid] = false;
                return false;
            }
            var data = {
                a: akey,
                c: encodeURIComponent(ct),
                id: frameid,
                rpnm: ''
            };
            if (replyto || replyto == 0) data['rp'] = replyto, data['rpnm'] = o.cmindex[replyto] /*判断是否是回复*/
            else data['rp'] = 'false';
            o.rq(o.mainpath + 'c.php?a=sm', data, {
                success: function(m) {
                    var j = JSON.parse(m),
                        mrid = '';
                    if (j.code == 1) {
                        var elem = o.s(el);
                        if (j.reply) {
                            var tp = o.csic(j.data, aid),
                                mainrid = j.data.parentrid,
                                sube = o.s(o.replylist[aid]['con'].rpl('mrid', mainrid)); /*获得子评论操作container*/
                            sube.innerHTML = tp + sube.innerHTML;
                            mrid = mainrid;
                        } else {
                            var tp = o.cic(j.data, aid).rpl('ReplyContent', '').rpl('MoreSubBtn', '');
                            elem.innerHTML = tp + elem.innerHTML;
                        }
                        o.cmindex[j.data.rid] = j.data.name; /*储存rid对应的用户*/
                        cb(j, mrid); /*callback*/
                    } else {
                        alert(j.msg || '评论发布失败');
                    }
                    o.sendqueue[aid] = false;
                },
                failed: function(m) {
                    alert('评论发布失败');
                    o.sendqueue[aid] = false;
                }
            }, 'post');
        }
    },
    mr: function(akey, mrid, rpart, aid, el, ifsub = false, cb) { /*展开更多-(akey,mrid，rpart,aid,元素id,是否展开的是子回复，callback<sub>(是否还有更多,目前切割到了哪里,mrid))*/
        var o = this,
            frameid = o.framesid[akey],
            data = ifsub ? {
                sub: 'y',
                mrid: mrid,
                pt: rpart,
                id: frameid,
                a: akey
            } : {
                id: frameid,
                a: akey,
                sub: 'n',
                pt: o.mainparts[aid]
            };
        o.rq(o.mainpath + 'c.php?a=mr', data, {
            success: function(m) {
                var j = JSON.parse(m);
                if (j.code == 1) {
                    var cm = j.data.r;
                    if (ifsub) {
                        var sube = o.s(el.rpl('mrid', mrid)),
                            subtp = o.replyrender(cm, false, aid)['subtp'];
                        sube.innerHTML += subtp; /*更新回复*/
                        cb(j.data.rcut, j.data.rpart, mrid);
                    } else { /*主评论展开模式*/
                        var e = o.s(el),
                            cmlist = o.floorrender(j.data, aid),
                            ifcut = j.cut;
                        e.innerHTML += cmlist; /*更新主楼层*/
                        o.mainparts[aid] = j.part;
                        var tp = ifcut ? o.tpmd[aid]['bottommore'] : o.tpmd[aid]['bottom'].rpl('CommentsNum', j.cmnum); /*切割了的话就加载更多*/
                        tp = tp.rpl('Comments', cmlist); /*渲染评论*/
                        cb(ifcut, tp);
                    }
                }
            },
            failed: function(m) {}
        }, 'post');
    }
};
CB.ini(); /*先初始化*/
CB.const();