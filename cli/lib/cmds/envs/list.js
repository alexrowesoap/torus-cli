'use strict';

var Promise = require('es6-promise').Promise;

var Command = require('../../cli/command');
var flags = require('../../flags');
var envs = require('../../envs');
var auth = require('../../middleware/auth');
var target = require('../../middleware/target');

var cmd = new Command(
  'envs',
  'list environments',
  function (ctx) {
    return new Promise(function (resolve, reject) {
      envs.list.execute(ctx).then(function (payload) {
        envs.list.output.success(null, payload);

        resolve(true);
      }).catch(function (err) {
        envs.list.output.failure();
        reject(err);
      });
    });
  }
);

cmd.hook('pre', auth());
cmd.hook('pre', target());

flags.add(cmd, 'org');
flags.add(cmd, 'project', {
  description: 'list environments for a particular project'
});

module.exports = cmd;
